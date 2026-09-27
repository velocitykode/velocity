package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	testsync "github.com/velocitykode/velocity/testing"
)

// leveledLine is one line a levelLogger recorded.
type leveledLine struct {
	level string
	msg   string
	kvs   []any
}

// levelLogger records every line with its level and fields. Safe for
// concurrent use by the worker's pumps and the test goroutine.
type levelLogger struct {
	mu    sync.Mutex
	lines []leveledLine
}

func (l *levelLogger) record(level, msg string, kvs []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, leveledLine{level: level, msg: msg, kvs: append([]any(nil), kvs...)})
}

func (l *levelLogger) Debug(msg string, kvs ...any) { l.record("debug", msg, kvs) }
func (l *levelLogger) Info(msg string, kvs ...any)  { l.record("info", msg, kvs) }
func (l *levelLogger) Warn(msg string, kvs ...any)  { l.record("warn", msg, kvs) }
func (l *levelLogger) Error(msg string, kvs ...any) { l.record("error", msg, kvs) }
func (l *levelLogger) Fatal(msg string, kvs ...any) { l.record("fatal", msg, kvs) }

func (l *levelLogger) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }

// errorLines returns the lines recorded at error level.
func (l *levelLogger) errorLines() []leveledLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []leveledLine
	for _, line := range l.lines {
		if line.level == "error" {
			out = append(out, line)
		}
	}
	return out
}

// field returns the value logged under key, and whether it was logged.
func (line leveledLine) field(key string) (any, bool) {
	for i := 0; i+1 < len(line.kvs); i += 2 {
		if line.kvs[i] == key {
			return line.kvs[i+1], true
		}
	}
	return nil, false
}

// hasValue reports whether any field of the line holds want.
func (line leveledLine) hasValue(want any) bool {
	for i := 1; i < len(line.kvs); i += 2 {
		if line.kvs[i] == want {
			return true
		}
	}
	return false
}

// logJobSeq numbers the jobs of these tests so no two share an ID.
var logJobSeq atomic.Int64

// runWorkerUntilFailed pushes job, runs a worker with maxRetries attempts,
// the logger and dispatch (nil for none) until the memory driver records
// the job failed, and stops the worker.
func runWorkerUntilFailed(t *testing.T, job Job, maxRetries int, logger *levelLogger, dispatch func(context.Context, interface{}) error) {
	t.Helper()
	const queueName = "failure-log"
	d := newStartedMemoryDriver(t)
	if err := d.PushCtx(context.Background(), job, queueName); err != nil {
		t.Fatalf("push: %v", err)
	}
	w := NewWorker(d, queueName, func(j Job) error { return j.Handle() },
		WithInterval(5*time.Millisecond), WithMaxRetries(maxRetries),
		WithBackoff(func(int) time.Duration { return time.Millisecond }),
		WithWorkerLogger(logger))
	if dispatch != nil {
		w.SetEventDispatcher(dispatch)
	}
	w.Start(context.Background())
	defer w.Stop()
	testsync.Eventually(t, func() bool {
		failed, err := d.GetFailed(queueName)
		return err == nil && len(failed) > 0
	}, 5*time.Second, "job failed for good")
	w.Stop()
}

// TestWorker_PermanentFailureLoggedOnceWithoutDispatcher asserts a worker
// with no event dispatcher writes exactly one error line for a job that
// exhausts its retries, naming the job type, its queue, its attempts and
// the job's own error, and no error line per failed attempt.
func TestWorker_PermanentFailureLoggedOnceWithoutDispatcher(t *testing.T) {
	logger := &levelLogger{}
	job := &plainFailingJob{ID: fmt.Sprintf("log-%d", logJobSeq.Add(1))}
	runWorkerUntilFailed(t, job, 2, logger, nil)

	lines := logger.errorLines()
	if len(lines) != 1 {
		t.Fatalf("error lines = %d, want 1: %+v", len(lines), lines)
	}
	line := lines[0]
	if !line.hasValue(normalizeJobType(fmt.Sprintf("%T", job))) {
		t.Errorf("error line does not name the job type: %+v", line)
	}
	if !line.hasValue("failure-log") {
		t.Errorf("error line does not name the queue: %+v", line)
	}
	if attempts, ok := line.field("attempts"); !ok || attempts != 2 {
		t.Errorf("attempts = %v, want 2: %+v", attempts, line)
	}
	logged, _ := line.field("error")
	if err, ok := logged.(error); !ok || !errors.Is(err, errSelfReportedBoom) {
		t.Errorf("error field = %v, want the job's own error", logged)
	}
}

// TestWorker_PermanentFailureNotLoggedWithDispatcher asserts a worker with
// an event dispatcher writes no error line for a job that exhausts its
// retries: queue.job.failed carries the failure to the failure-report bridge,
// which reports it once.
func TestWorker_PermanentFailureNotLoggedWithDispatcher(t *testing.T) {
	logger := &levelLogger{}
	var failedEvents atomic.Int32
	dispatch := func(_ context.Context, event interface{}) error {
		if _, ok := event.(*JobFailed); ok {
			failedEvents.Add(1)
		}
		return nil
	}
	runWorkerUntilFailed(t, &plainFailingJob{ID: fmt.Sprintf("log-%d", logJobSeq.Add(1))}, 2, logger, dispatch)

	testsync.Eventually(t, func() bool { return failedEvents.Load() == 1 }, 2*time.Second, "queue.job.failed dispatched")
	if lines := logger.errorLines(); len(lines) != 0 {
		t.Fatalf("error lines = %d, want 0: %+v", len(lines), lines)
	}
}

// TestWorker_SelfReportedFailureNotLoggedWithoutDispatcher asserts a job
// whose Failed hook reported its own failure (a queued listener's does) is
// not logged a second time by a worker with no dispatcher.
func TestWorker_SelfReportedFailureNotLoggedWithoutDispatcher(t *testing.T) {
	logger := &levelLogger{}
	runWorkerUntilFailed(t, &selfReportingJob{ID: fmt.Sprintf("log-%d", logJobSeq.Add(1))}, 1, logger, nil)

	if lines := logger.errorLines(); len(lines) != 0 {
		t.Fatalf("error lines = %d, want 0: %+v", len(lines), lines)
	}
}
