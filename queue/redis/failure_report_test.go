package redis

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/queue"
	testsync "github.com/velocitykode/velocity/testing"
)

var errRedisSelfReported = errors.New("redis self-reported job exploded")

// redisSelfReportingJob fails every run; its Failed hook records that it
// reported the failure itself, the way a queued event listener's hook does.
type redisSelfReportingJob struct {
	ID       string `json:"id"`
	reported atomic.Bool
}

func (j *redisSelfReportingJob) Handle() error         { return errRedisSelfReported }
func (j *redisSelfReportingJob) Failed(error)          { j.reported.Store(true) }
func (j *redisSelfReportingJob) FailureReported() bool { return j.reported.Load() }
func (j *redisSelfReportingJob) MaxAttempts() int      { return 1 }

func init() {
	queue.RegisterJob(func(data []byte) (*redisSelfReportingJob, error) {
		j := &redisSelfReportingJob{}
		if err := json.Unmarshal(data, j); err != nil {
			return nil, err
		}
		return j, nil
	})
}

// quietLogger drops worker log lines.
type quietLogger struct{}

func (quietLogger) Info(string, ...any)  {}
func (quietLogger) Warn(string, ...any)  {}
func (quietLogger) Error(string, ...any) {}

// TestRedisDriver_JobFailedMarkedWhenHookReported asserts that on the redis
// driver, which fails a job through Failed and runs the job's hook on the
// instance the worker popped, the job.failed event carries the job's own
// error marked reported when the hook reported it, so the failure-report
// bridge does not report the failure a second time.
func TestRedisDriver_JobFailedMarkedWhenHookReported(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()
	driver, err := NewRedisDriver(queue.RedisConfig{Host: mr.Host(), Port: mr.Port(), DB: "0"})
	if err != nil {
		t.Fatalf("new redis driver: %v", err)
	}
	defer driver.Shutdown(context.Background())

	const queueName = "redis-failure-report"
	if err := driver.PushCtx(context.Background(), &redisSelfReportingJob{ID: "r1"}, queueName); err != nil {
		t.Fatalf("push: %v", err)
	}

	var (
		mu     sync.Mutex
		failed []*queue.JobFailed
	)
	w := queue.NewWorker(driver, queueName, func(j queue.Job) error { return j.Handle() },
		queue.WithInterval(5*time.Millisecond), queue.WithWorkerLogger(quietLogger{}))
	w.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		if e, ok := event.(*queue.JobFailed); ok {
			mu.Lock()
			failed = append(failed, e)
			mu.Unlock()
		}
		return nil
	})
	w.Start(context.Background())
	defer w.Stop()
	testsync.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(failed) > 0
	}, 5*time.Second, "worker dispatches job.failed")
	w.Stop()

	mu.Lock()
	defer mu.Unlock()
	if len(failed) != 1 {
		t.Fatalf("job.failed dispatched %d times, want 1", len(failed))
	}
	if !errors.Is(failed[0].Err, errRedisSelfReported) {
		t.Errorf("Err = %v, want the job's own error", failed[0].Err)
	}
	if !contract.IsReported(failed[0].Err) {
		t.Error("Err not marked reported although the redis driver ran the reporting hook")
	}
}

// redisPanickingHookJob panics in its Failed hook.
type redisPanickingHookJob struct {
	ID string `json:"id"`
}

func (j *redisPanickingHookJob) Handle() error {
	return errors.New("redis panicking hook job exploded")
}
func (j *redisPanickingHookJob) Failed(error) { panic("redis hook exploded for " + j.ID) }

// TestRedisDriver_FailedHookPanicContained asserts the redis driver stores
// the failed job and then contains a panic in the job's Failed hook,
// returning queue.ErrFailedHookPanicked instead of unwinding the caller.
func TestRedisDriver_FailedHookPanicContained(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()
	driver, err := NewRedisDriver(queue.RedisConfig{Host: mr.Host(), Port: mr.Port(), DB: "0"})
	if err != nil {
		t.Fatalf("new redis driver: %v", err)
	}
	defer driver.Shutdown(context.Background())

	const queueName = "redis-hook-panic"
	job := &redisPanickingHookJob{ID: "p1"}
	err = driver.Failed(job, errors.New("boom"), queueName)
	if !errors.Is(err, queue.ErrFailedHookPanicked) {
		t.Fatalf("Failed returned %v, want ErrFailedHookPanicked", err)
	}
	failed, err := mr.List("velocity:queue:" + queueName + ":failed")
	if err != nil || len(failed) != 1 {
		t.Errorf("failed list has %d entries (err %v), want 1", len(failed), err)
	}
}
