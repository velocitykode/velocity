package hostile

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestModes_String(t *testing.T) {
	want := []string{"panic", "block", "reenter"}
	for i, m := range Modes() {
		if m.String() != want[i] {
			t.Errorf("Modes()[%d].String() = %q, want %q", i, m.String(), want[i])
		}
	}
	if Mode(99).String() != "unknown" {
		t.Errorf("an unknown mode is named %q", Mode(99).String())
	}
}

func TestCode_Panic(t *testing.T) {
	c := New(t, Panic, nil)
	p := Within(t, Deadline, c.Run)
	if p != PanicValue {
		t.Fatalf("Run panicked with %v, want PanicValue", p)
	}
	select {
	case <-c.Entered():
	default:
		t.Fatal("Entered is not closed after Run")
	}
	c.Disarm()
	if p := Within(t, Deadline, c.Run); p != nil {
		t.Fatalf("a disarmed Run panicked: %v", p)
	}
	if c.Calls() != 2 {
		t.Fatalf("Calls() = %d, want 2", c.Calls())
	}
}

func TestCode_BlockUntilRelease(t *testing.T) {
	c := New(t, Block, nil)
	returned := make(chan struct{})
	go func() {
		c.Run()
		close(returned)
	}()
	<-c.Entered()
	select {
	case <-returned:
		t.Fatal("a Block Run returned before Release")
	case <-time.After(20 * time.Millisecond):
	}
	c.Release()
	c.Release() // idempotent
	<-returned
	Within(t, Deadline, c.Run) // a Run after Release does not block
}

func TestCode_ReenterOnce(t *testing.T) {
	var calls int
	var c *Code
	c = New(t, Reenter, func() {
		calls++
		c.Run() // re-entering the same code must not recurse
	})
	Within(t, Deadline, c.Run)
	Within(t, Deadline, c.Run)
	if calls != 1 {
		t.Fatalf("the re-entry func ran %d times, want 1", calls)
	}
	if c.Calls() != 3 {
		t.Fatalf("Calls() = %d, want 3", c.Calls())
	}
}

func TestCode_Nil(t *testing.T) {
	var c *Code
	c.Run() // a nil Code runs nothing
}

func TestNew_CleanupReleasesBlock(t *testing.T) {
	var c *Code
	t.Run("inner", func(t *testing.T) {
		c = New(t, Block, nil)
	})
	Within(t, Deadline, c.Run)
}

func TestLogger_SelectedMethodsRunTheCode(t *testing.T) {
	c := New(t, Panic, nil)
	l := NewLogger(c, Warn)
	if p := Within(t, Deadline, func() { l.Info("info line", "k", 1) }); p != nil {
		t.Fatalf("an unselected method ran the code: %v", p)
	}
	if p := Within(t, Deadline, func() { l.Warn("warn line") }); p != PanicValue {
		t.Fatalf("Warn panicked with %v, want PanicValue", p)
	}
	if l.Count(Info, "info line") != 1 || l.Count(Warn, "warn line") != 0 {
		t.Fatalf("lines = %+v, want only the info line", l.Lines())
	}
}

func TestLogger_WithKeepsBehaviourAndBindsFields(t *testing.T) {
	c := New(t, Panic, nil)
	l := NewLogger(c, Error)
	bound := l.With("a", 1).With("b", 2)
	bound.Info("hello", "c", 3)
	lines := l.Lines()
	if len(lines) != 1 || fmt.Sprint(lines[0].KVs) != "[a 1 b 2 c 3]" {
		t.Fatalf("lines = %+v, want one line with a,b,c", lines)
	}
	if p := Within(t, Deadline, func() { bound.Error("boom") }); p != PanicValue {
		t.Fatalf("a bound Error panicked with %v, want PanicValue", p)
	}
	withCode := New(t, Panic, nil)
	if p := Within(t, Deadline, func() { NewLogger(withCode, With).With("x", 1) }); p != PanicValue {
		t.Fatalf("a selected With panicked with %v, want PanicValue", p)
	}
}

func TestLogger_EveryMethodByDefault(t *testing.T) {
	c := New(t, Panic, nil)
	l := NewLogger(c)
	for _, call := range []func(){
		func() { l.Debug("m") }, func() { l.Info("m") }, func() { l.Warn("m") },
		func() { l.Error("m") }, func() { l.Fatal("m") }, func() { l.With("k", "v") },
	} {
		if p := Within(t, Deadline, call); p != PanicValue {
			t.Fatalf("a method did not run the code: %v", p)
		}
	}
}

func TestDispatcher(t *testing.T) {
	c := New(t, Reenter, nil)
	d := NewDispatcher(c)
	if err := d.Dispatch(context.Background(), "ev"); err != nil {
		t.Fatal(err)
	}
	if got := d.Events(); len(got) != 1 || got[0] != "ev" {
		t.Fatalf("Events() = %v", got)
	}
	pc := New(t, Panic, nil)
	pd := NewDispatcher(pc)
	if p := Within(t, Deadline, func() { _ = pd.Dispatch(context.Background(), "ev") }); p != PanicValue {
		t.Fatalf("Dispatch panicked with %v", p)
	}
	if len(pd.Events()) != 0 {
		t.Fatal("an event whose dispatch panicked was recorded")
	}
}

func TestValue_StringAndErrorRunTheCode(t *testing.T) {
	c := New(t, Panic, nil)
	v := NewValue(c, "text")
	// fmt recovers a panicking String itself and prints the panic; the
	// code still ran (a Block or Reenter value is not contained by fmt).
	if s := fmt.Sprint(v); !strings.Contains(s, PanicValue) || c.Calls() != 1 {
		t.Fatalf("formatting the value gave %q after %d runs, want the panic after 1", s, c.Calls())
	}
	var err error = v
	if p := Within(t, Deadline, func() { _ = err.Error() }); p != PanicValue {
		t.Fatalf("Error panicked with %v", p)
	}
	c.Disarm()
	if v.String() != "text" || v.Error() != "text" {
		t.Fatal("the value lost its text")
	}
}

// fakeTB records Errorf so a test can check that Within reports a hang.
type fakeTB struct {
	testing.TB
	mu     sync.Mutex
	errors []string
}

func (f *fakeTB) Helper() {}
func (f *fakeTB) Errorf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
}

func TestWithin_ReportsAHang(t *testing.T) {
	c := New(t, Block, nil)
	fake := &fakeTB{TB: t}
	Within(fake, 20*time.Millisecond, c.Run)
	c.Release()
	if len(fake.errors) != 1 || !strings.Contains(fake.errors[0], "did not return within") ||
		!strings.Contains(fake.errors[0], "goroutine") {
		t.Fatalf("Within reported %q, want a hang with goroutine stacks", fake.errors)
	}
}

func TestIsolated_RunsInAChild(t *testing.T) {
	ran := false
	Isolated(t, func() { ran = true })
	if os.Getenv(isolatedEnv) == "" && ran {
		t.Fatal("fn ran in the parent process")
	}
	if os.Getenv(isolatedEnv) != "" && !ran {
		t.Fatal("fn did not run in the child process")
	}
}

func TestIsolated_Subtest(t *testing.T) {
	t.Run("sub name", func(t *testing.T) {
		Isolated(t, func() {})
	})
}

// TestIsolated_EscapedPanicFails checks the failure path: in the child it
// panics on a goroutine of its own, which kills the child; the parent sees
// that as an error carrying the panic.
func TestIsolated_EscapedPanicFails(t *testing.T) {
	if os.Getenv(isolatedEnv) == t.Name() {
		go func() { panic(PanicValue) }()
		time.Sleep(10 * time.Second)
		return
	}
	out, err := runIsolated(t.Name())
	if err == nil {
		t.Fatalf("an escaped panic did not fail the child:\n%s", out)
	}
	if !strings.Contains(string(out), PanicValue) {
		t.Fatalf("the child's output does not carry the panic:\n%s", out)
	}
}

func TestRunPattern(t *testing.T) {
	if got := runPattern("TestA/sub_(x)"); got != `^TestA$/^sub_\(x\)$` {
		t.Fatalf("runPattern = %q", got)
	}
}

func TestDeadline_Scales(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	base := 5 * time.Second
	if raceEnabled {
		base *= 2
	}
	if got := deadline(env(nil)); got != base {
		t.Errorf("deadline = %v, want %v", got, base)
	}
	if got := deadline(env(map[string]string{"CI": "true"})); got != 2*base {
		t.Errorf("deadline under CI = %v, want %v", got, 2*base)
	}
	if got := deadline(env(map[string]string{deadlineEnv: "90s", "CI": "true"})); got != 90*time.Second {
		t.Errorf("deadline with the override = %v, want 90s", got)
	}
	if got := deadline(env(map[string]string{deadlineEnv: "nonsense"})); got != base {
		t.Errorf("deadline with a bad override = %v, want %v", got, base)
	}
}

// deadlineTB records failures and reports a deadline, for the helpers' own
// failure paths.
type deadlineTB struct {
	testing.TB
	end    time.Time
	failed bool
}

func (f *deadlineTB) Helper()                     {}
func (f *deadlineTB) Errorf(string, ...any)       { f.failed = true }
func (f *deadlineTB) Deadline() (time.Time, bool) { return f.end, !f.end.IsZero() }

func TestClamp_EndsBeforeTheTestTimeout(t *testing.T) {
	if got := clamp(&deadlineTB{}, time.Hour); got != time.Hour {
		t.Errorf("clamp without a deadline = %v, want 1h", got)
	}
	if got := clamp(&deadlineTB{end: time.Now().Add(3 * time.Second)}, time.Hour); got > 2*time.Second {
		t.Errorf("clamp = %v, want at most 2s", got)
	}
	if got := clamp(&deadlineTB{end: time.Now()}, time.Hour); got <= 0 {
		t.Errorf("clamp past the deadline = %v, want a small positive wait", got)
	}
}

func TestEventually(t *testing.T) {
	var n atomic.Int32
	go func() { //safe-goroutine: bumps a counter the test polls
		for range 3 {
			n.Add(1)
		}
	}()
	if !Eventually(t, Deadline, "three bumps", func() bool { return n.Load() == 3 }) {
		t.Fatal("Eventually gave up on a condition that held")
	}
	f := &deadlineTB{}
	if Eventually(f, 20*time.Millisecond, "never", func() bool { return false }) || !f.failed {
		t.Fatal("Eventually did not fail on a condition that never held")
	}
}

func TestAwaitEntered(t *testing.T) {
	c := New(t, Block, nil)
	go c.Run() //safe-goroutine: blocks until the cleanup releases it
	if !c.AwaitEntered(t) {
		t.Fatal("AwaitEntered missed a running Code")
	}
	f := &deadlineTB{end: time.Now().Add(1100 * time.Millisecond)}
	if New(t, Block, nil).AwaitEntered(f) || !f.failed {
		t.Fatal("AwaitEntered did not fail for a Code that never ran")
	}
}
