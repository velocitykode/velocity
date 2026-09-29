//go:build unix

package scheduler

import (
	"context"
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
		s.SetEventDispatcher(func(_ context.Context, ev interface{}) error {
			switch ev.(type) {
			case *ScheduledTaskFinished, *ScheduledTaskFailed:
				panic("dispatcher broke")
			}
			return nil
		})
		s.Command("sleep", "0.05").RunInBackground().Name("bg.dispatch").Cron("* * * * *")
		runDone := make(chan error, 1)
		go func() { runDone <- s.Run(context.Background()) }()
		time.Sleep(500 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown = %v: the run was never released", err)
		}
		<-runDone
	})
}
