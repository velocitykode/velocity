package eventemit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// namedEvent is a framework-style event with a name.
type namedEvent struct{ name string }

func (e namedEvent) Name() string { return e.name }

// plainEvent is an app-defined event that does not implement contract.Event.
type plainEvent struct{ ID int }

// line is one call recorded by recordingLogger.
type line struct {
	level string
	msg   string
	kvs   []any
}

// recordingLogger records every line written through it.
type recordingLogger struct {
	mu    sync.Mutex
	lines []line
}

func (l *recordingLogger) add(level, msg string, kvs []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line{level: level, msg: msg, kvs: append([]any(nil), kvs...)})
}

func (l *recordingLogger) Debug(msg string, kvs ...any) { l.add("debug", msg, kvs) }
func (l *recordingLogger) Info(msg string, kvs ...any)  { l.add("info", msg, kvs) }
func (l *recordingLogger) Warn(msg string, kvs ...any)  { l.add("warn", msg, kvs) }
func (l *recordingLogger) Error(msg string, kvs ...any) { l.add("error", msg, kvs) }
func (l *recordingLogger) Fatal(msg string, kvs ...any) { l.add("fatal", msg, kvs) }
func (l *recordingLogger) With(kvs ...any) contract.Logger {
	return contract.BindFields(l, kvs...)
}

// at returns the lines written at level.
func (l *recordingLogger) at(level string) []line {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []line
	for _, ln := range l.lines {
		if ln.level == level {
			out = append(out, ln)
		}
	}
	return out
}

// field returns the value logged under key.
func (ln line) field(key string) any {
	for i := 0; i+1 < len(ln.kvs); i += 2 {
		if k, _ := ln.kvs[i].(string); k == key {
			return ln.kvs[i+1]
		}
	}
	return nil
}

var errListener = errors.New("listener failed")

// nilCtx is a nil context, which every entry point accepts.
var nilCtx context.Context

func failing(context.Context, any) error { return errListener }

// Each failure is counted; the first failure of each event name is logged
// at warn level, once, naming the event and the failure.
func TestFailures_Record_CountsEveryFailureLogsFirstPerName(t *testing.T) {
	var f Failures
	logger := &recordingLogger{}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		f.Record(ctx, logger, errListener, namedEvent{"cache.hit"})
		f.Record(ctx, logger, errListener, &namedEvent{"mail.completed"})
	}
	if got := f.Count(); got != 6 {
		t.Errorf("Count = %d, want 6", got)
	}
	warns := logger.at("warn")
	if len(warns) != 2 {
		t.Fatalf("warn lines = %d, want 2 (one per event name): %+v", len(warns), warns)
	}
	for i, want := range []string{"cache.hit", "mail.completed"} {
		if warns[i].msg != FailureMessage {
			t.Errorf("line %d message = %q, want %q", i, warns[i].msg, FailureMessage)
		}
		if got := warns[i].field("event"); got != want {
			t.Errorf("line %d event = %v, want %q", i, got, want)
		}
		if got, _ := warns[i].field("error").(error); !errors.Is(got, errListener) {
			t.Errorf("line %d error = %v, want the failure", i, warns[i].field("error"))
		}
	}
}

// With no logger the line goes through the standalone fallback logger.
func TestFailures_Record_NilLoggerUsesTheFallback(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	var f Failures
	f.Record(context.Background(), nil, errListener, namedEvent{"csrf.session.missed"})
	if got := out.String(); !strings.Contains(got, "WARN "+FailureMessage) || !strings.Contains(got, "event=csrf.session.missed") {
		t.Errorf("fallback output = %q, want one warn line naming the event", got)
	}
}

// An app-defined event with no name is logged, and handed to the hook,
// under its Go type; the hook receives the event itself.
func TestFailures_Record_EventWithoutNameUsesItsType(t *testing.T) {
	var f Failures
	var got []any
	f.SetHook(func(_ error, event any) { got = append(got, event) })
	logger := &recordingLogger{}
	f.Record(context.Background(), logger, errListener, plainEvent{ID: 7})
	f.Record(context.Background(), logger, errListener, namedEvent{""})

	warns := logger.at("warn")
	if len(warns) != 2 {
		t.Fatalf("warn lines = %d, want 2: %+v", len(warns), warns)
	}
	if name := warns[0].field("event"); name != "eventemit.plainEvent" {
		t.Errorf("event = %v, want eventemit.plainEvent", name)
	}
	if name := warns[1].field("event"); name != "eventemit.namedEvent" {
		t.Errorf("event = %v, want eventemit.namedEvent", name)
	}
	if len(got) != 2 || got[0] != (plainEvent{ID: 7}) {
		t.Errorf("hook events = %v, want the app event first", got)
	}
}

// The hook sees every failure with its error and event; nil removes it.
func TestFailures_SetHook(t *testing.T) {
	var f Failures
	var calls atomic.Int32
	var seen error
	f.SetHook(func(err error, _ any) { calls.Add(1); seen = err })
	f.Record(context.Background(), &recordingLogger{}, errListener, namedEvent{"a"})
	f.Record(context.Background(), &recordingLogger{}, errListener, namedEvent{"a"})
	if calls.Load() != 2 || !errors.Is(seen, errListener) {
		t.Fatalf("hook calls = %d, err = %v; want 2 calls with the failure", calls.Load(), seen)
	}
	f.SetHook(nil)
	f.Record(context.Background(), &recordingLogger{}, errListener, namedEvent{"a"})
	if calls.Load() != 2 {
		t.Errorf("hook called after SetHook(nil): %d calls", calls.Load())
	}
}

// A panicking hook is recovered and counted as one more failure, its first
// panic is logged at error level once, and it never re-enters the policy
// (the hook is not called for its own panic).
func TestFailures_HookPanicIsRecoveredCountedAndNotReentered(t *testing.T) {
	var f Failures
	var calls atomic.Int32
	f.SetHook(func(error, any) {
		calls.Add(1)
		panic("hook broke")
	})
	logger := &recordingLogger{}
	for i := 0; i < 3; i++ {
		f.Record(context.Background(), logger, errListener, namedEvent{"queue.job.completed"})
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("hook calls = %d, want 3 (one per failure, none for its own panics)", got)
	}
	if got := f.Count(); got != 6 {
		t.Errorf("Count = %d, want 6 (3 failures + 3 hook panics)", got)
	}
	errs := logger.at("error")
	if len(errs) != 1 || errs[0].msg != HookPanicMessage || errs[0].field("panic") != "hook broke" {
		t.Errorf("error lines = %+v, want one %q line with the panic", errs, HookPanicMessage)
	}
	if warns := logger.at("warn"); len(warns) != 1 {
		t.Errorf("warn lines = %d, want 1", len(warns))
	}
}

// Past the bound on remembered names a new name is still counted and
// handed to the hook, only not logged.
func TestFailures_LoggedNamesAreBounded(t *testing.T) {
	var f Failures
	var hooked atomic.Int32
	f.SetHook(func(error, any) { hooked.Add(1) })
	logger := &recordingLogger{}
	for i := 0; i < maxLoggedNames+5; i++ {
		f.Record(context.Background(), logger, errListener, namedEvent{fmt.Sprintf("e%d", i)})
	}
	if got := len(logger.at("warn")); got != maxLoggedNames {
		t.Errorf("warn lines = %d, want %d", got, maxLoggedNames)
	}
	if f.Count() != maxLoggedNames+5 || hooked.Load() != maxLoggedNames+5 {
		t.Errorf("Count = %d, hook calls = %d, want %d each", f.Count(), hooked.Load(), maxLoggedNames+5)
	}
}

// Recording records each failure once and marks it; the mark survives %w
// wrapping and errors.Join, keeps errors.Is/As working, and an Emitter
// holding the recording function does not record it again.
func TestFailures_Recording_RecordsOnceAndMarks(t *testing.T) {
	var app Failures
	logger := &recordingLogger{}
	dispatch := app.Recording(failing, logger)

	err := dispatch(nilCtx, namedEvent{"router.request.completed"})
	if !errors.Is(err, errListener) || !Recorded(err) {
		t.Fatalf("err = %v, want the failure, marked recorded", err)
	}
	if app.Count() != 1 {
		t.Fatalf("Count = %d, want 1", app.Count())
	}
	for _, wrapped := range []error{
		fmt.Errorf("component: %w", err),
		errors.Join(errors.New("other"), fmt.Errorf("deep: %w", fmt.Errorf("deeper: %w", err))),
	} {
		if !Recorded(wrapped) || !errors.Is(wrapped, errListener) {
			t.Errorf("Recorded(%v) = false or lost errors.Is, want the mark to survive wrapping", wrapped)
		}
	}
	if Recorded(errListener) || Recorded(nil) {
		t.Error("Recorded reports an unmarked error or nil as recorded")
	}

	var e Emitter
	e.Set(dispatch)
	e.Emit(context.Background(), namedEvent{"router.request.completed"})
	e.Fail(context.Background(), fmt.Errorf("wrapped: %w", err), namedEvent{"router.request.completed"})
	if app.Count() != 2 {
		t.Errorf("app Count = %d, want 2 (one per dispatch, none from the emitter)", app.Count())
	}
	if e.FailureCount() != 0 {
		t.Errorf("emitter own count = %d, want 0: a recorded failure is not recorded again", e.FailureCount())
	}
	if got := len(logger.at("warn")); got != 1 {
		t.Errorf("warn lines = %d, want 1", got)
	}
}

// Recording of nil is nil, and a successful dispatch returns nil.
func TestFailures_Recording_NilAndSuccess(t *testing.T) {
	var f Failures
	if f.Recording(nil, nil) != nil {
		t.Error("Recording(nil) is not nil")
	}
	ok := f.Recording(func(ctx context.Context, _ any) error {
		if ctx == nil {
			t.Error("dispatch got a nil ctx")
		}
		return nil
	}, nil)
	if err := ok(nilCtx, namedEvent{"x"}); err != nil || f.Count() != 0 {
		t.Errorf("err = %v, Count = %d; want nil and 0", err, f.Count())
	}
}

// The zero Emitter has no dispatcher: Emit reports false, Installed is
// false, and nothing is recorded.
func TestEmitter_ZeroValue(t *testing.T) {
	var e Emitter
	if e.Installed() || e.Dispatcher() != nil {
		t.Error("zero Emitter reports a dispatcher")
	}
	if e.Emit(context.Background(), namedEvent{"x"}) {
		t.Error("Emit on the zero Emitter reported a dispatcher")
	}
	e.Fail(context.Background(), nil, namedEvent{"x"})
	if e.FailureCount() != 0 {
		t.Errorf("FailureCount = %d, want 0", e.FailureCount())
	}
}

// Set installs and nil removes the dispatcher; Emit hands the event and
// ctx (Background for nil) to it and records a failure in the emitter's
// own Failures through the component's logger.
func TestEmitter_SetEmitFail(t *testing.T) {
	var e Emitter
	logger := &recordingLogger{}
	e.UseLogger(func() contract.Logger { return logger })
	var got []any
	e.Set(func(ctx context.Context, event any) error {
		if ctx == nil {
			t.Error("dispatcher got a nil ctx")
		}
		got = append(got, event)
		return errListener
	})
	if !e.Installed() {
		t.Fatal("Installed = false after Set")
	}
	if !e.Emit(nilCtx, namedEvent{"scheduler.task.completed"}) {
		t.Fatal("Emit reported no dispatcher")
	}
	if len(got) != 1 || e.FailureCount() != 1 || len(logger.at("warn")) != 1 {
		t.Errorf("delivered %d, count %d, warn lines %d; want 1 each", len(got), e.FailureCount(), len(logger.at("warn")))
	}
	e.Set(nil)
	if e.Installed() || e.Emit(context.Background(), namedEvent{"x"}) {
		t.Error("dispatcher still installed after Set(nil)")
	}
}

// A logger source that returns nil, or a nil source, means the fallback.
func TestEmitter_UseLoggerNil(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	var e Emitter
	e.Set(failing)
	e.UseLogger(func() contract.Logger { return nil })
	e.Emit(context.Background(), namedEvent{"a"})
	e.UseLogger(nil)
	e.Emit(context.Background(), namedEvent{"b"})
	if got := strings.Count(out.String(), "WARN "+FailureMessage); got != 2 {
		t.Errorf("fallback warn lines = %d, want 2: %q", got, out.String())
	}
}

// Share records into the shared Failures; Share(nil) returns the emitter
// to its own, without panicking.
func TestEmitter_Share(t *testing.T) {
	var e Emitter
	e.UseLogger(func() contract.Logger { return &recordingLogger{} })
	e.Set(failing)
	shared := &Failures{}
	e.Share(shared)
	e.Emit(context.Background(), namedEvent{"a"})
	if shared.Count() != 1 || e.FailureCount() != 1 {
		t.Errorf("shared = %d, emitter reads %d; want 1 and 1", shared.Count(), e.FailureCount())
	}
	e.Share(nil)
	e.Emit(context.Background(), namedEvent{"a"})
	if shared.Count() != 1 || e.FailureCount() != 1 {
		t.Errorf("after Share(nil): shared = %d, own = %d; want 1 and 1", shared.Count(), e.FailureCount())
	}
}

// Set, Share and UseLogger race with Emit and Fail from many goroutines:
// no data race (run under -race), no lost or duplicated count: every
// failed dispatch is recorded exactly once, in the Failures the emitter
// held when it saw the failure.
func TestEmitter_ConcurrentConfigureAndEmit(t *testing.T) {
	var e Emitter
	a, b := &Failures{}, &Failures{}
	var dispatched atomic.Uint64
	fail := func(context.Context, any) error { dispatched.Add(1); return errListener }
	e.Set(fail)
	e.Share(a)

	const emitters, perEmitter = 16, 500
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var configurers sync.WaitGroup
	configurers.Add(1)
	go func() {
		defer configurers.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				e.Share(b)
			} else {
				e.Share(a)
			}
			e.Set(fail)
			e.UseLogger(func() contract.Logger { return &recordingLogger{} })
		}
	}()
	for g := 0; g < emitters; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perEmitter; i++ {
				e.Emit(context.Background(), namedEvent{fmt.Sprintf("e%d", g%4)})
				_ = e.Installed()
			}
		}(g)
	}
	wg.Wait()
	close(stop)
	configurers.Wait()

	if got, want := a.Count()+b.Count(), dispatched.Load(); got != want {
		t.Errorf("recorded %d failures for %d failed dispatches, want exactly one each", got, want)
	}
	if dispatched.Load() != emitters*perEmitter {
		t.Errorf("dispatched %d, want %d", dispatched.Load(), emitters*perEmitter)
	}
}

// Recording shared by many goroutines, with a hook, counts every failure
// once and calls the hook once per failure.
func TestFailures_ConcurrentRecording(t *testing.T) {
	var f Failures
	var hooked atomic.Uint64
	f.SetHook(func(error, any) { hooked.Add(1) })
	dispatch := f.Recording(failing, &recordingLogger{})
	var e Emitter
	e.Set(dispatch)

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				e.Emit(context.Background(), namedEvent{"orm.query.completed"})
			}
		}()
	}
	wg.Wait()
	if f.Count() != 3200 || hooked.Load() != 3200 || e.FailureCount() != 0 {
		t.Errorf("Count = %d, hook = %d, emitter own = %d; want 3200, 3200, 0", f.Count(), hooked.Load(), e.FailureCount())
	}
}
