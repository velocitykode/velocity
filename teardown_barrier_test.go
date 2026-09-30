package velocity

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/velocitykode/velocity/scheduler"
)

// A drain step that times out leaves the teardown waiting for the child's
// admitted work, not closing the services beneath it: a scheduler task
// still running past the caller's deadline keeps the queue open until it
// finishes, and the teardown goes on in order after it.
func TestShutdown_ClosesNothingBeneathADrainThatTimedOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, err := NewTestApp()
		if err != nil {
			t.Fatalf("NewTestApp: %v", err)
		}
		queue := &queueCloseProbe{QueueDriver: a.Queue}
		a.Queue = queue
		s, ok := a.Scheduler.(*scheduler.Scheduler)
		if !ok {
			t.Fatalf("Scheduler is %T", a.Scheduler)
		}
		entered := make(chan struct{})
		release := make(chan struct{})
		released := false
		// Every goroutine of the bubble must end, whatever failed.
		defer func() {
			if !released {
				close(release)
			}
			_ = a.Shutdown(context.Background())
		}()
		s.Named("barrier.task", func() {
			close(entered)
			<-release
		}).Cron("* * * * *")
		ran := make(chan error, 1)
		go func() { ran <- s.Run(context.Background()) }()
		<-entered

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if err := a.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Shutdown = %v, want the deadline while a task runs", err)
		}
		synctest.Wait()
		if n := queue.shutdowns.Load(); n != 0 {
			t.Fatalf("the queue was closed %d times while a scheduler task still ran", n)
		}

		close(release)
		released = true
		// The teardown's result, which a later Shutdown returns, is its
		// steps' own: the steps after the barrier got the caller's expired
		// ctx and forced at once, so it may report that deadline.
		_ = a.Shutdown(context.Background())
		if n := queue.shutdowns.Load(); n != 1 {
			t.Fatalf("queue closes = %d, want 1", n)
		}
		<-ran
	})
}
