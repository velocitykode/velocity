package queue

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// isPanics is an error whose Is and Error methods panic.
type isPanics struct{}

func (isPanics) Error() string { panic("Error broke") }
func (isPanics) Is(error) bool { panic("Is broke") }

// selfUnwrap unwraps to itself.
type selfUnwrap struct{}

func (e *selfUnwrap) Error() string { return "loop" }
func (e *selfUnwrap) Unwrap() error { return e }

// popErrDriver fails every pop with err, and fails a job for good by
// counting it.
type popErrDriver struct {
	noMarshalDriver
	err    error
	job    Job
	pops   atomic.Int32
	failed atomic.Int32
}

func (d *popErrDriver) PopCtx(context.Context, string) (Job, error) {
	if d.pops.Add(1) == 1 && d.job != nil {
		return d.job, nil
	}
	if d.job != nil {
		return nil, nil
	}
	return nil, d.err
}

func (d *popErrDriver) FailedCtx(context.Context, Job, error, string) error {
	d.failed.Add(1)
	return nil
}

// A pop error whose Is panics, or whose chain loops, is a worker error
// like any other: the loop logs it, backs off and polls again, and Stop
// returns.
func TestWorker_HostilePopErrorKeepsPolling(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"Is panics", isPanics{}},
		{"loop", &selfUnwrap{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &popErrDriver{err: tc.err}
			w := NewWorker(d, "default", func(Job) error { return nil }, WithInterval(time.Millisecond), WithWorkerLogger(nullLogger{}))
			w.Start(context.Background())
			hostile.Eventually(t, hostile.Deadline, "three pops", func() bool { return d.pops.Load() >= 3 })
			var err error
			hostile.Within(t, hostile.Deadline, func() { err = w.Stop(context.Background()) })
			if err != nil {
				t.Fatalf("Stop = %v", err)
			}
		})
	}
}

// A job whose handler returns an error whose Is panics, or whose chain
// loops, while the worker runs is the job's failure: it is failed for good
// (one attempt allowed), not taken for a shutdown abort.
func TestWorker_HostileHandlerErrorFailsTheJob(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"Is panics", isPanics{}},
		{"loop", &selfUnwrap{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &popErrDriver{job: hashCountJob{N: 1}}
			w := NewWorker(d, "default", func(Job) error { return tc.err },
				WithInterval(time.Millisecond), WithMaxRetries(1), WithWorkerLogger(nullLogger{}))
			w.Start(context.Background())
			hostile.Eventually(t, hostile.Deadline, "the job failed", func() bool { return d.failed.Load() == 1 })
			hostile.Within(t, hostile.Deadline, func() { _ = w.Stop(context.Background()) })
		})
	}
}
