//go:build unix

package scheduler

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// runDispatchPanicProbe runs one RunInBackground task on a scheduler whose
// event dispatcher panics on the task's completion events, then shuts down: it exits 0 when
// the process survived and the task's run was released (Shutdown returned
// nil), 7 when Shutdown did not.
func runDispatchPanicProbe() {
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
	go func() { _ = s.Run(context.Background()) }()
	time.Sleep(500 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		os.Exit(7)
	}
	os.Exit(0)
}

// A dispatcher that panics on a RunInBackground task's events is
// contained on the task's completion goroutine, including from its
// recovery: the process survives and the run is released.
func TestRunInBackground_PanickingDispatcherIsContained(t *testing.T) {
	if os.Getenv("VELOCITY_SCHEDULER_DISPATCH_PROBE") != "" {
		runDispatchPanicProbe()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunInBackground_PanickingDispatcherIsContained$")
	cmd.Env = append(os.Environ(), "VELOCITY_SCHEDULER_DISPATCH_PROBE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		code := -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		if code == 7 {
			t.Fatal("contained, but the run was never released: Shutdown did not return nil")
		}
		tail := out
		if len(tail) > 1500 {
			tail = tail[len(tail)-1500:]
		}
		t.Fatalf("probe process died (exit %d): the dispatcher panic was not contained\n%s", code, tail)
	}
}
