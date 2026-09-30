package events

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// debouncedFailure is a FailureEvent delivered later by a debouncing
// dispatcher, so the failure-report bridge sees it on the timer goroutine.
type debouncedFailure struct{}

func (debouncedFailure) Name() string                        { return "debounced.failure" }
func (debouncedFailure) FailureError() error                 { return errors.New("background work broke") }
func (debouncedFailure) FailureSource() contract.ErrorSource { return contract.ErrorSourceJob }

// detachedScenario delivers one failing event detached, the way its name
// says, with a failure recorder and a failure reporter that panic as the
// name says, and returns how many times the recorder ran. It returns once
// the delivery has ended: the recorder ran (it is the delivery's last
// step) and the contained panic's fallback line was written to out (the
// recovery finished), and it fails the test if the panicking code never
// ran.
func detachedScenario(t *testing.T, name string, out interface{ String() string }) (recorded int32) {
	var calls atomic.Int32
	panics := hostile.New(t, hostile.Panic, nil)
	recorderPanics := strings.HasPrefix(name, "recorder/")
	reporterPanics := strings.HasPrefix(name, "reporter/")
	wire := func(d *DefaultDispatcher) {
		d.SetDetachedFailureRecorder(func(context.Context, error, any) {
			calls.Add(1)
			if recorderPanics {
				panics.Run()
			}
		})
		d.SetFailureReporter(func(context.Context, any, error) {
			if reporterPanics {
				panics.Run()
			}
		})
		d.Listen("evt", failingListener{})
		d.Listen("debounced.failure", failingListener{})
	}
	ctx := context.Background()
	var stop func()
	switch strings.SplitN(name, "/", 2)[1] {
	case "after":
		d := NewDispatcher()
		wire(d)
		_ = d.DispatchAfter(ctx, "evt", time.Millisecond)
	case "async":
		d := NewDispatcher()
		wire(d)
		_ = d.DispatchAsync(ctx, "evt")
	case "debounce":
		d := NewDebouncingDispatcher(time.Millisecond)
		wire(d.DefaultDispatcher)
		_ = d.Dispatch(ctx, "evt")
		stop = d.Stop
	case "debounce-failure-event":
		d := NewDebouncingDispatcher(time.Millisecond)
		wire(d.DefaultDispatcher)
		_ = d.Dispatch(ctx, debouncedFailure{})
		stop = d.Stop
	case "coalesce":
		d := NewCoalescingDispatcher(time.Millisecond)
		wire(d.DefaultDispatcher)
		_ = d.Dispatch(ctx, "evt")
		stop = d.Stop
	}
	hostile.Eventually(t, hostile.Deadline, "the recorder ran and the contained panic was written", func() bool {
		return calls.Load() > 0 && strings.Contains(out.String(), detachedPanicMessage)
	})
	if panics.Calls() == 0 {
		t.Fatal("the panicking recorder or reporter never ran")
	}
	if stop != nil {
		stop()
	}
	return calls.Load()
}

// A failure recorder or failure reporter that panics on a detached
// delivery (a DispatchAfter or DispatchAsync without a queue, a debounced
// or coalesced delivery) has no caller left to receive the panic: it is
// contained on the goroutine delivering the event, written through the
// fallback logger, and the delivery still reaches the recorder once. A
// reporter's panic never kills the process or skips the accounting. Each
// scenario runs in a child process, so a panic that escapes fails it
// alone.
func TestDetachedDelivery_PanickingRecorderOrReporterIsContained(t *testing.T) {
	for _, name := range []string{
		"recorder/after", "recorder/async", "recorder/debounce", "recorder/coalesce",
		"reporter/after", "reporter/async", "reporter/debounce", "reporter/debounce-failure-event", "reporter/coalesce",
	} {
		t.Run(name, func(t *testing.T) {
			hostile.Isolated(t, func() {
				out := fallbacklogtest.Capture(t)
				if got := detachedScenario(t, name, out); got != 1 {
					t.Errorf("recorder runs = %d, want 1", got)
				}
				if !strings.Contains(out.String(), detachedPanicMessage) {
					t.Errorf("no fallback line %q for the contained panic in\n%s", detachedPanicMessage, out.String())
				}
			})
		})
	}
}
