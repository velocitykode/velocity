package queue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/trace"
)

// fieldLine is one line a fieldRecorder recorded, bound pairs first.
type fieldLine struct {
	msg string
	kvs []any
}

func (l fieldLine) field(key string) any {
	for i := 0; i+1 < len(l.kvs); i += 2 {
		if k, ok := l.kvs[i].(string); ok && k == key {
			return l.kvs[i+1]
		}
	}
	return nil
}

type fieldSink struct {
	mu    sync.Mutex
	lines []fieldLine
}

// fieldRecorder records every line with its bound and own key-value
// pairs; With binds pairs written before each line's own.
type fieldRecorder struct {
	sink  *fieldSink
	bound []any
}

func (r fieldRecorder) Debug(msg string, kvs ...any) { r.add(msg, kvs) }
func (r fieldRecorder) Info(msg string, kvs ...any)  { r.add(msg, kvs) }
func (r fieldRecorder) Warn(msg string, kvs ...any)  { r.add(msg, kvs) }
func (r fieldRecorder) Error(msg string, kvs ...any) { r.add(msg, kvs) }
func (r fieldRecorder) Fatal(msg string, kvs ...any) { r.add(msg, kvs) }

func (r fieldRecorder) With(kvs ...any) contract.Logger {
	return fieldRecorder{sink: r.sink, bound: append(append([]any(nil), r.bound...), kvs...)}
}

func (r fieldRecorder) add(msg string, kvs []any) {
	all := append(append([]any(nil), r.bound...), kvs...)
	r.sink.mu.Lock()
	defer r.sink.mu.Unlock()
	r.sink.lines = append(r.sink.lines, fieldLine{msg: msg, kvs: all})
}

func (r fieldRecorder) snapshot() []fieldLine {
	r.sink.mu.Lock()
	defer r.sink.mu.Unlock()
	return append([]fieldLine(nil), r.sink.lines...)
}

// identifiedFailingJob fails every run and names itself through JobID.
type identifiedFailingJob struct {
	ID string `json:"id"`
}

func (j *identifiedFailingJob) Handle() error    { return errors.New("identified job exploded") }
func (j *identifiedFailingJob) Failed(error)     {}
func (j *identifiedFailingJob) JobID() string    { return j.ID }
func (j *identifiedFailingJob) MaxAttempts() int { return 2 }

// Every line the worker writes for a job, a retry, the failure the worker
// loop logs after each attempt, carries the job's id, type and queue and
// the trace the job runs under.
func TestWorker_JobLinesCarryTheJobAndItsTrace(t *testing.T) {
	const producerTrace, producerSpan = "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	q := NewMemoryDriver()
	// The delayed-job tick moves the released retry back onto the queue.
	q.Start()
	defer func() { _ = q.Shutdown(context.Background()) }()
	logger := fieldRecorder{sink: &fieldSink{}}
	w := NewWorker(q, "default", func(j Job) error { return j.Handle() },
		WithWorkerLogger(logger),
		WithBackoff(func(int) time.Duration { return 0 }),
		WithInterval(time.Millisecond),
	)
	producer := trace.WithTrace(context.Background(), producerTrace, producerSpan)
	if err := q.PushCtx(producer, &identifiedFailingJob{ID: "job-7"}, "default"); err != nil {
		t.Fatalf("push: %v", err)
	}

	w.Start(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for {
		failed, _ := q.GetFailed("default")
		if len(failed) > 0 {
			break
		}
		if time.Now().After(deadline) {
			w.Stop(context.Background())
			t.Fatalf("job never failed permanently: %+v", logger.snapshot())
		}
		time.Sleep(time.Millisecond)
	}
	w.Stop(context.Background())

	var jobLines []fieldLine
	for _, l := range logger.snapshot() {
		if l.msg == "Worker started" || l.msg == "Worker stopped" {
			continue
		}
		jobLines = append(jobLines, l)
	}
	var sawRetry bool
	for _, l := range jobLines {
		sawRetry = sawRetry || l.msg == "Retrying job"
		for key, want := range map[string]string{
			"job_id":   "job-7",
			"job_type": "identifiedFailingJob",
			"queue":    "default",
			"trace_id": producerTrace,
		} {
			if got := l.field(key); got != want {
				t.Errorf("line %q %s = %v, want %q (%v)", l.msg, key, got, want, l.kvs)
			}
		}
		if got := l.field("type"); got != nil {
			t.Errorf("line %q still carries type = %v", l.msg, got)
		}
	}
	if !sawRetry {
		t.Fatalf("no retry line in %+v", jobLines)
	}
}
