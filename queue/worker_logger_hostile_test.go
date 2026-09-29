package queue

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// The worker's logger is user code, written to from its pumps on every
// job (With) and on each retry, failure and worker error. One that panics
// must not kill a pump or skip the cleanup after a failure: a failing job
// still ends in the failed list, the next job still runs, Stop returns,
// and the lines reach the fallback logger instead.
func TestWorker_PanickingLoggerIsContained(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	const queueName = "hostile-worker-logger"
	d := newStartedMemoryDriver(t)
	code := hostile.New(t, hostile.Panic, nil)
	var ran atomic.Int64
	w := NewWorker(d, queueName, func(j Job) error {
		ran.Add(1)
		return j.Handle()
	},
		WithInterval(5*time.Millisecond), WithMaxRetries(2),
		WithBackoff(func(int) time.Duration { return time.Millisecond }),
		WithWorkerLogger(hostile.NewLogger(code)))

	if err := d.PushCtx(context.Background(), &plainFailingJob{ID: fmt.Sprintf("hostile-log-%d", logJobSeq.Add(1))}, queueName); err != nil {
		t.Fatalf("push: %v", err)
	}
	w.Start(context.Background())
	t.Cleanup(w.Stop)

	hostile.Eventually(t, hostile.Deadline, "the failing job recorded as failed", func() bool {
		failed, err := d.GetFailed(queueName)
		return err == nil && len(failed) == 1
	})
	before := ran.Load()
	if err := d.PushCtx(context.Background(), &plainFailingJob{ID: fmt.Sprintf("hostile-log-next-%d", logJobSeq.Add(1))}, queueName); err != nil {
		t.Fatalf("push next: %v", err)
	}
	hostile.Eventually(t, hostile.Deadline, "the worker running the next job", func() bool {
		return ran.Load() > before
	})
	hostile.Within(t, hostile.Deadline, w.Stop)
	if n := out.Count("INFO", "Retrying job") + out.Count("ERROR", "Job failed"); n == 0 {
		t.Errorf("no worker line reached the fallback logger:\n%s", out.String())
	}
}
