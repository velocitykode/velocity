package velocity

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/scheduler"
)

// panickingWarnLogger panics on every warn line.
type panickingWarnLogger struct{ levelLogger }

func (*panickingWarnLogger) Warn(string, ...any) { panic("logger broke") }

// A logger that panics on the warning Bootstrap writes when events are
// disabled but event callbacks were registered does not stop the
// bootstrap: every later step still runs.
func TestBootstrap_PanickingLoggerDoesNotSkipLaterSteps(t *testing.T) {
	a, err := NewTestApp(WithoutEvents())
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	a.Services.Log = &panickingWarnLogger{}
	scheduled := false
	a.Events(func(events.Dispatcher) {}).
		Schedule(func(scheduler.TaskScheduler) { scheduled = true })

	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("logger panic escaped Bootstrap: %v", p)
			}
		}()
		if err := a.Bootstrap(); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
	}()
	if !scheduled {
		t.Error("the Schedule callback did not run")
	}
}

// A bootstrap callback that panics still panics out of Bootstrap, and a
// later Bootstrap returns the failure instead of reporting success for a
// bootstrap that never finished.
func TestBootstrap_PanickingCallbackIsStickyError(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	a.Routes(func(*chain.Routing) { panic("routes broke") })

	func() {
		defer func() {
			if p := recover(); p == nil {
				t.Fatal("Bootstrap did not panic")
			}
		}()
		_ = a.Bootstrap()
	}()
	err = a.Bootstrap()
	var rp contract.RecoveredPanic
	if !errors.As(err, &rp) || rp.Recovered() != "routes broke" {
		t.Fatalf("second Bootstrap = %v, want the recovered panic", err)
	}
}
