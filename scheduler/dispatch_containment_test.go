//go:build unix

package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// A dispatcher that panics on a RunInBackground task's events is
// contained on the task's completion goroutine, including from its
// recovery: the process survives and the run is released (Shutdown
// returns nil). It runs isolated, since an uncontained panic there ends
// the process.
func TestRunInBackground_PanickingDispatcherIsContained(t *testing.T) {
	hostile.Isolated(t, func() {
		s := New()
		// Only the completion events panic, so the command starts and its
		// completion goroutine meets the panics, in its hooks and its recovery.
		var panics atomic.Int32
		panicked := make(chan struct{})
		var once sync.Once
		s.SetEventDispatcher(func(_ context.Context, ev interface{}) error {
			switch ev.(type) {
			case *ScheduledTaskFinished, *ScheduledTaskFailed:
				panics.Add(1)
				once.Do(func() { close(panicked) })
				panic("dispatcher broke")
			}
			return nil
		})
		s.Command("sleep", "0.05").RunInBackground().Name("bg.dispatch").Cron("* * * * *")
		runDone := make(chan error, 1)
		go func() { runDone <- s.Run(context.Background()) }()
		select {
		case <-panicked:
		case <-time.After(hostile.Deadline):
			t.Fatal("the command's completion never reached the dispatcher")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown = %v: the run was never released", err)
		}
		<-runDone
		if panics.Load() == 0 {
			t.Fatal("the dispatcher never panicked; the test proves nothing")
		}
	})
}
