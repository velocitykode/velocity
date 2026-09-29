package log

import (
	"context"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// The redacting logger against a hostile value (formatted through its
// String) and a hostile redactor: no panic escapes from a value, a blocked
// one holds up no other writer, re-entry returns, and once the user code
// behaves the line is redacted and written.
func TestRedactingLogger_HostileSweep(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run("value/"+mode.String(), func(t *testing.T) {
			inner := hostile.NewLogger(nil)
			var wrapped Logger
			code := hostile.New(t, mode, func() { wrapped.Warn("reentered") })
			wrapped = WithRedactors(inner, RedactorFunc(func(s string) string { return s }))
			v := hostile.NewValue(code, "value")
			runHostile(t, mode, code, func() { wrapped.Info("line", "k", v) }, func() { wrapped.Info("other") })
			if inner.Count(hostile.Info, "line") == 0 {
				t.Error("no line written once the value behaved")
			}
		})
		t.Run("redactor/"+mode.String(), func(t *testing.T) {
			inner := hostile.NewLogger(nil)
			var wrapped Logger
			code := hostile.New(t, mode, func() { wrapped.Warn("reentered") })
			wrapped = WithRedactors(inner, RedactorFunc(func(s string) string { code.Run(); return s }))
			// A redactor is called synchronously on the caller's goroutine:
			// its panic reaches that caller, and the wrapper holds no state.
			runHostileCaller(t, mode, code, func() { wrapped.Info("line") })
			hostile.Within(t, hostile.Deadline, func() { wrapped.Info("line") })
			if inner.Count(hostile.Info, "line") == 0 {
				t.Error("no line written once the redactor behaved")
			}
		})
	}
}

// A stack logger against a hostile child: every other child gets the line,
// no panic escapes, a blocked child holds up no other writer, re-entry
// returns, and the hostile child gets lines again once it behaves.
func TestStackLogger_HostileChildSweep(t *testing.T) {
	for _, mode := range hostile.Modes() {
		for _, name := range []string{"Info", "With"} {
			t.Run(mode.String()+"/"+name, func(t *testing.T) {
				var stack *StackLogger
				code := hostile.New(t, mode, func() { stack.Warn("reentered") })
				bad, good := hostile.NewLogger(code), hostile.NewLogger(nil)
				stack = NewStackLogger(bad, good)
				call := func() { stack.Info("line") }
				if name == "With" {
					call = func() { stack.With("k", "v").Info("line") }
				}
				// Every line reaches the hostile child, so no other line can
				// pass a blocked one; the stack holds no lock of its own, so
				// there is nothing else for a block to hold up.
				runHostile(t, mode, code, call, nil)
				if good.Count(hostile.Info, "line") < 2 {
					t.Errorf("good child lines = %d, want the hostile run's and the retry's", good.Count(hostile.Info, "line"))
				}
				if bad.Count(hostile.Info, "line") == 0 {
					t.Error("the hostile child got no line once it behaved")
				}
			})
		}
	}
}

// A channel whose driver factory panics, blocks or calls back into the
// manager: the manager holds no lock across the factory, so other channels
// and Shutdown proceed, re-entry returns, and once the factory behaves the
// channel is created.
func TestManager_HostileDriverFactorySweep(t *testing.T) {
	var mu sync.Mutex
	codes := map[string]*hostile.Code{}
	Drivers().Register("hostile-sweep", func(_ context.Context, cfg LogConfig) (Logger, error) {
		mu.Lock()
		code := codes[cfg.Config["id"].(string)]
		mu.Unlock()
		code.Run()
		return NewNullLogger(), nil
	})
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			m := NewManager(LoggingConfig{Default: "console", Channels: map[string]ChannelConfig{
				"console": {Driver: "null"},
				"hostile": {Driver: "hostile-sweep", Options: map[string]any{"id": t.Name()}},
			}})
			code := hostile.New(t, mode, func() {
				_, _ = m.Channel("console")
				_ = m.Shutdown(context.Background())
			})
			mu.Lock()
			codes[t.Name()] = code
			mu.Unlock()
			runHostileCaller(t, mode, code, func() { _, _ = m.Channel("hostile") })
			hostile.Within(t, hostile.Deadline, func() {
				if _, err := m.Channel("hostile"); err != nil {
					t.Errorf("channel once the factory behaved: %v", err)
				}
			})
		})
	}
}

// runHostile runs call against user code that is contained: a panic must
// not escape it. In Block mode it checks that other runs while call is
// blocked (when other is not nil), then releases it. Afterwards the code is disarmed and call runs
// again, as the retry.
func runHostile(t *testing.T, mode hostile.Mode, code *hostile.Code, call, other func()) {
	t.Helper()
	if mode == hostile.Block {
		go call()
		<-code.Entered()
		if other != nil {
			hostile.Within(t, hostile.Deadline, other)
		}
	} else if p := hostile.Within(t, hostile.Deadline, call); p != nil {
		t.Fatalf("a panic escaped: %v", p)
	}
	code.Disarm()
	code.Release()
	if p := hostile.Within(t, hostile.Deadline, call); p != nil {
		t.Fatalf("retry panicked: %v", p)
	}
}

// runHostileCaller runs call against user code the component calls
// synchronously for its caller: a panic reaches the caller, as the user
// code's own, and must leave the component usable; a block holds up only
// the caller. Afterwards the code is disarmed for the retry.
func runHostileCaller(t *testing.T, mode hostile.Mode, code *hostile.Code, call func()) {
	t.Helper()
	switch mode {
	case hostile.Block:
		go call()
		<-code.Entered()
	case hostile.Panic:
		if p := hostile.Within(t, hostile.Deadline, call); p != hostile.PanicValue {
			t.Fatalf("panic = %v, want the user code's own to reach the caller", p)
		}
	default:
		if p := hostile.Within(t, hostile.Deadline, call); p != nil {
			t.Fatalf("a panic escaped: %v", p)
		}
	}
	code.Disarm()
	code.Release()
}

var _ contract.Logger = (*StackLogger)(nil)
