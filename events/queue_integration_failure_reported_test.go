package events

import (
	"errors"
	"sync/atomic"
	"testing"
)

// TestEventListenerJob_FailureReported asserts FailureReported describes
// the most recent Failed call: true only when the installed reporter says
// it reported the failure; false with no reporter installed, a nil error, a
// reporter that declined the failure, and a reporter that panicked, so the
// worker's job.failed bridge reports those failures.
func TestEventListenerJob_FailureReported(t *testing.T) {
	defer setFailureReporter(nil)
	boom := errors.New("listener exploded")
	job := &EventListenerJob{ListenerType: "audit"}

	if job.FailureReported() {
		t.Fatal("FailureReported() = true before any Failed call")
	}

	setFailureReporter(nil)
	job.Failed(boom)
	if job.FailureReported() {
		t.Error("FailureReported() = true with no reporter installed")
	}

	var calls atomic.Int32
	var accept atomic.Bool
	setFailureReporter(func(j *EventListenerJob, err error) bool {
		calls.Add(1)
		if j != job || !errors.Is(err, boom) {
			t.Errorf("reporter got (%p, %v), want (%p, %v)", j, err, job, boom)
		}
		return accept.Load()
	})
	accept.Store(true)
	job.Failed(boom)
	if !job.FailureReported() {
		t.Error("FailureReported() = false after the reporter reported the failure")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("reporter called %d times, want 1", n)
	}

	accept.Store(false)
	job.Failed(boom)
	if job.FailureReported() {
		t.Error("FailureReported() = true after the reporter declined the failure")
	}

	accept.Store(true)
	job.Failed(nil)
	if job.FailureReported() {
		t.Error("FailureReported() = true after a Failed call with a nil error")
	}

	job.Failed(boom)
	setFailureReporter(nil)
	job.Failed(boom)
	if job.FailureReported() {
		t.Error("FailureReported() kept an earlier call's answer after a reporter-less Failed")
	}

	setFailureReporter(func(*EventListenerJob, error) bool { panic("sink exploded") })
	job.Failed(boom)
	if job.FailureReported() {
		t.Error("FailureReported() = true after the reporter panicked")
	}
}
