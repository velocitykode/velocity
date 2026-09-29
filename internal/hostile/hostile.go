// Package hostile is test infrastructure: user code that fails on purpose.
// It gives a test the pieces of user code a framework component calls (a
// logger, an event dispatch func, a value whose String and Error run code)
// in a form that panics, blocks until released, or calls back into the
// component, plus the helpers that turn the resulting crash or hang into a
// test failure.
//
// Every fake is built on Code, the behaviour itself: a domain fake (an
// event listener, a queue dispatcher, a stream) calls Code.Run first in
// each method, then does its normal work.
//
// Only _test.go files import this package; a guard test enforces it. It
// depends on the standard library and the contract leaf only.
package hostile

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// Mode is what a piece of hostile user code does when it runs.
type Mode int

const (
	// Panic panics with PanicValue.
	Panic Mode = iota
	// Block blocks until Code.Release is called.
	Block
	// Reenter calls the re-entry func given to New, once, on the calling
	// goroutine, then returns.
	Reenter
)

// PanicValue is the value a Panic Code panics with.
const PanicValue = "hostile: user code panicked"

// Modes returns every Mode, for a table that sweeps a component's entry
// points against each one.
func Modes() []Mode { return []Mode{Panic, Block, Reenter} }

// String returns the mode's name, for subtest names.
func (m Mode) String() string {
	switch m {
	case Panic:
		return "panic"
	case Block:
		return "block"
	case Reenter:
		return "reenter"
	}
	return "unknown"
}

// Code is one piece of hostile user code. Safe for concurrent use. A nil
// *Code runs nothing.
type Code struct {
	mode    Mode
	reenter func()

	calls       atomic.Int64
	disarmed    atomic.Bool
	reentered   atomic.Bool
	entered     chan struct{}
	enteredOnce sync.Once
	release     chan struct{}
	releaseOnce sync.Once
}

// New returns a Code with the given mode. reenter is the call a Reenter
// Code makes into the component under test; the other modes ignore it. A
// Block Code is released when the test ends, so no goroutine it holds
// outlives the test.
func New(t testing.TB, mode Mode, reenter func()) *Code {
	t.Helper()
	c := &Code{
		mode:    mode,
		reenter: reenter,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	t.Cleanup(c.Release)
	return c
}

// Run is the behaviour: the fakes call it before their own work. It counts
// the call, marks the code entered, then, unless the code is disarmed,
// panics, blocks until Release, or calls the re-entry func. The re-entry
// func runs at the first Run only, so a component that calls the same user
// code again from the re-entered call cannot recurse without end.
func (c *Code) Run() {
	if c == nil {
		return
	}
	c.calls.Add(1)
	c.enteredOnce.Do(func() { close(c.entered) })
	if c.disarmed.Load() {
		return
	}
	switch c.mode {
	case Panic:
		panic(PanicValue)
	case Block:
		<-c.release
	case Reenter:
		if c.reenter != nil && c.reentered.CompareAndSwap(false, true) {
			c.reenter()
		}
	}
}

// Entered returns a channel closed when Run first starts, so a test can
// wait until the user code is running (for example, blocked) before it
// calls the component's other entry points.
func (c *Code) Entered() <-chan struct{} { return c.entered }

// Release unblocks every Run blocked now and every later one. Idempotent.
func (c *Code) Release() {
	c.releaseOnce.Do(func() { close(c.release) })
}

// Disarm makes every later Run do nothing but count, so a test can check
// that the component still works once the user code behaves (a retry
// works). It does not release a Run blocked now; call Release for that.
func (c *Code) Disarm() { c.disarmed.Store(true) }

// Calls returns how many times Run was called.
func (c *Code) Calls() int { return int(c.calls.Load()) }

// Method names a logger method, to select the ones that run a Logger's
// behaviour.
type Method string

// The logger methods.
const (
	Debug Method = "Debug"
	Info  Method = "Info"
	Warn  Method = "Warn"
	Error Method = "Error"
	Fatal Method = "Fatal"
	With  Method = "With"
)

// Line is one line a Logger recorded: the method, the message, and the
// bound pairs followed by the line's own. Values are kept as passed, never
// formatted.
type Line struct {
	Level Method
	Msg   string
	KVs   []any
}

// Logger is a contract.Logger whose selected methods run its Code first,
// then record the line (a line whose Code panicked is not recorded).
type Logger struct {
	code   *Code
	on     map[Method]bool
	rec    *lineRecord
	fields []any
}

type lineRecord struct {
	mu    sync.Mutex
	lines []Line
}

var _ contract.Logger = (*Logger)(nil)

// NewLogger returns a Logger whose methods in on (every method when on is
// empty) run c. With runs c when it is selected, and returns a Logger that
// keeps the behaviour, records into the same lines, and binds the pairs.
func NewLogger(c *Code, on ...Method) *Logger {
	l := &Logger{code: c, rec: &lineRecord{}}
	if len(on) > 0 {
		l.on = make(map[Method]bool, len(on))
		for _, m := range on {
			l.on[m] = true
		}
	}
	return l
}

func (l *Logger) selected(m Method) bool { return l.on == nil || l.on[m] }

func (l *Logger) line(m Method, msg string, kvs []any) {
	if l.selected(m) {
		l.code.Run()
	}
	all := make([]any, 0, len(l.fields)+len(kvs))
	all = append(append(all, l.fields...), kvs...)
	l.rec.mu.Lock()
	l.rec.lines = append(l.rec.lines, Line{Level: m, Msg: msg, KVs: all})
	l.rec.mu.Unlock()
}

func (l *Logger) Debug(msg string, kvs ...any) { l.line(Debug, msg, kvs) }
func (l *Logger) Info(msg string, kvs ...any)  { l.line(Info, msg, kvs) }
func (l *Logger) Warn(msg string, kvs ...any)  { l.line(Warn, msg, kvs) }
func (l *Logger) Error(msg string, kvs ...any) { l.line(Error, msg, kvs) }
func (l *Logger) Fatal(msg string, kvs ...any) { l.line(Fatal, msg, kvs) }

// With runs the behaviour when With is selected, then returns a Logger
// with kvs bound before each line's own pairs.
func (l *Logger) With(kvs ...any) contract.Logger {
	if l.selected(With) {
		l.code.Run()
	}
	fields := make([]any, 0, len(l.fields)+len(kvs))
	fields = append(append(fields, l.fields...), kvs...)
	return &Logger{code: l.code, on: l.on, rec: l.rec, fields: fields}
}

// Lines returns the recorded lines, from this Logger and every Logger its
// With returned.
func (l *Logger) Lines() []Line {
	l.rec.mu.Lock()
	defer l.rec.mu.Unlock()
	return append([]Line(nil), l.rec.lines...)
}

// Count returns how many recorded lines have level m and message msg.
func (l *Logger) Count(m Method, msg string) int {
	n := 0
	for _, line := range l.Lines() {
		if line.Level == m && line.Msg == msg {
			n++
		}
	}
	return n
}

// Dispatcher is an event dispatch func that runs its Code, then records
// the event and returns nil.
type Dispatcher struct {
	code   *Code
	mu     sync.Mutex
	events []any
}

// NewDispatcher returns a Dispatcher that runs c on every dispatch.
func NewDispatcher(c *Code) *Dispatcher { return &Dispatcher{code: c} }

// Dispatch is the dispatch func: pass d.Dispatch where a component takes
// func(ctx context.Context, event any) error.
func (d *Dispatcher) Dispatch(_ context.Context, event any) error {
	d.code.Run()
	d.mu.Lock()
	d.events = append(d.events, event)
	d.mu.Unlock()
	return nil
}

// Events returns the events dispatched so far (not those whose Code
// panicked).
func (d *Dispatcher) Events() []any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]any(nil), d.events...)
}

// Value is a value whose String and Error run its Code, then return its
// text: log it, format it, or return it as an error to reach user code
// through formatting.
type Value struct {
	code *Code
	text string
}

// NewValue returns a Value that runs c and then returns text.
func NewValue(c *Code, text string) Value { return Value{code: c, text: text} }

// String runs the behaviour and returns the text.
func (v Value) String() string {
	v.code.Run()
	return v.text
}

// Error runs the behaviour and returns the text.
func (v Value) Error() string {
	v.code.Run()
	return v.text
}
