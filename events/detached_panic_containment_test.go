package events

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// detachedChildEnv names the scenario a re-executed test binary runs.
const detachedChildEnv = "VELOCITY_DETACHED_PANIC_CHILD"

// debouncedFailure is a FailureEvent delivered later by a debouncing
// dispatcher, so the failure-report bridge sees it on the timer goroutine.
type debouncedFailure struct{}

func (debouncedFailure) Name() string                        { return "debounced.failure" }
func (debouncedFailure) FailureError() error                 { return errors.New("background work broke") }
func (debouncedFailure) FailureSource() contract.ErrorSource { return contract.ErrorSourceJob }

// detachedScenario delivers one failing event detached, the way its name
// says, with a failure recorder and a failure reporter that panic as the
// name says, and returns how many times the recorder ran.
func detachedScenario(name string) (recorded int32) {
	var calls atomic.Int32
	recorderPanics := strings.HasPrefix(name, "recorder/")
	reporterPanics := strings.HasPrefix(name, "reporter/")
	wire := func(d *DefaultDispatcher) {
		d.SetDetachedFailureRecorder(func(context.Context, error, any) {
			calls.Add(1)
			if recorderPanics {
				panic("recorder broke")
			}
		})
		d.SetFailureReporter(func(context.Context, any, error) {
			if reporterPanics {
				panic("reporter broke")
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
	waitFor(func() bool { return calls.Load() > 0 })
	// Give an escaped panic time to end the process.
	time.Sleep(50 * time.Millisecond)
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
// reporter's panic never kills the process or skips the accounting.
func TestDetachedDelivery_PanickingRecorderOrReporterIsContained(t *testing.T) {
	if name := os.Getenv(detachedChildEnv); name != "" {
		fmt.Printf("recorded=%d\n", detachedScenario(name))
		return
	}
	for _, name := range []string{
		"recorder/after", "recorder/async", "recorder/debounce", "recorder/coalesce",
		"reporter/after", "reporter/async", "reporter/debounce", "reporter/debounce-failure-event", "reporter/coalesce",
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run", "^TestDetachedDelivery_PanickingRecorderOrReporterIsContained$", "-test.count=1")
			cmd.Env = append(os.Environ(), detachedChildEnv+"="+name)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("the panic ended the process: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "recorded=1\n") {
				t.Errorf("recorder runs: want recorded=1 in\n%s", out)
			}
			if !strings.Contains(string(out), detachedPanicMessage) {
				t.Errorf("no fallback line %q for the contained panic in\n%s", detachedPanicMessage, out)
			}
		})
	}
}
