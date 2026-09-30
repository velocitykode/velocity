package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// failingLocker fails every Acquire with a backend error.
type failingLocker struct{}

var errLockerDown = errors.New("locker down")

func (failingLocker) Acquire(context.Context, string, time.Duration) (Lock, error) {
	return nil, errLockerDown
}

// A due task skipped because Locker.Acquire failed stays counted until
// its warning has been written, for both guards.
func TestRunDueJobs_AcquireFailureWarningKeepsTheRunCounted(t *testing.T) {
	for _, guard := range []string{"OnOneServer", "WithoutOverlapping"} {
		t.Run(guard, func(t *testing.T) {
			l := newGatedLogger()
			s := New()
			s.SetLogger(l)
			j := s.Named("drain.acquire."+guard, func() {}).Cron(fmt.Sprintf("%d * * * *", time.Now().Minute()))
			if guard == "OnOneServer" {
				j.OnOneServer()
			} else {
				j.WithoutOverlapping()
			}
			s.SetLocker(failingLocker{})

			var wg sync.WaitGroup
			wg.Add(1)
			go func() { defer wg.Done(); s.runDueJobs() }()
			select {
			case <-l.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("acquire-failure warning never written")
			}
			done := ticksIdle(s)
			assertStillCounted(t, done, "acquire-failure warning")
			close(l.gate)
			wg.Wait()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("run never released after its warning finished")
			}
		})
	}
}

// A logger that panics while writing the acquire-failure warning does not
// escape runDueJobs (it would kill the ticker goroutine, and the process),
// the run is released, and the line goes to the fallback.
func TestRunDueJobs_PanickingAcquireFailureWarningIsContained(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	l := newGatedLogger()
	l.panics = true
	s := New()
	s.SetLogger(l)
	s.Named("drain.acquire.panic", func() {}).Cron(fmt.Sprintf("%d * * * *", time.Now().Minute())).OnOneServer()
	s.SetLocker(failingLocker{})

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("runDueJobs panicked: %v", r)
			}
		}()
		s.runDueJobs()
	}()
	select {
	case <-ticksIdle(s):
	case <-time.After(2 * time.Second):
		t.Fatal("run never released after its warning panicked")
	}
	if n := fallback.Wait("WARN", "Skipping job: Locker.Acquire backend error", 1, 2*time.Second); n != 1 {
		t.Errorf("fallback lines = %d, want 1", n)
	}
}
