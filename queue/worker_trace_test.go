package queue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/trace"
)

type traceCapturingJob struct {
	ID       string
	captured chan struct {
		traceID  string
		spanID   string
		parentID string
	}
}

func (j *traceCapturingJob) Handle() error { return nil }
func (j *traceCapturingJob) HandleCtx(ctx context.Context) error {
	t, s, p := trace.GetTraceContext(ctx)
	j.captured <- struct {
		traceID  string
		spanID   string
		parentID string
	}{t, s, p}
	return nil
}
func (j *traceCapturingJob) Failed(err error) {}
func (j *traceCapturingJob) JobID() string    { return j.ID }

func waitForTrace(t *testing.T, ch <-chan struct {
	traceID  string
	spanID   string
	parentID string
}, timeout time.Duration) struct {
	traceID  string
	spanID   string
	parentID string
} {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(timeout):
		t.Fatal("timed out waiting for HandleCtx invocation")
		panic("unreachable")
	}
}

// TestWorker_RunsJobAsChildOfProducerSpan confirms PushCtx -> persist ->
// pop -> HandleCtx runs the job as a new span of the producer's trace whose
// parent is the producer's span (the span current when the job was pushed).
// Uses the MemoryDriver because it implements TraceAwareDriver and exercises
// the same Payload field as the database / redis drivers.
func TestWorker_RunsJobAsChildOfProducerSpan(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	job := &traceCapturingJob{
		ID: "trace-1",
		captured: make(chan struct {
			traceID  string
			spanID   string
			parentID string
		}, 1),
	}

	producerTrace := "4bf92f3577b34da6a3ce929d0e0e4736"
	producerCtx := trace.WithTrace(context.Background(), producerTrace, "00f067aa0ba902b7")
	producerCtx = trace.WithSpan(producerCtx, "b7ad6b7169203331")
	producerSpan := trace.GetSpanID(producerCtx)

	if err := q.PushCtx(producerCtx, job, "trace-queue"); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	worker := NewWorker(q, "trace-queue", func(j Job) error { return nil })
	worker.Start(context.Background())
	defer worker.Stop(context.Background())

	got := waitForTrace(t, job.captured, 5*time.Second)
	if got.traceID != producerTrace {
		t.Errorf("trace id: got %q want the producer's %q", got.traceID, producerTrace)
	}
	if got.spanID == "" || got.spanID == producerSpan {
		t.Errorf("span id: got %q, want a new span (producer span %q)", got.spanID, producerSpan)
	}
	if got.parentID != producerSpan {
		t.Errorf("parent id: got %q want the producer's span %q", got.parentID, producerSpan)
	}
}

// TestWorker_PayloadWithoutTraceStartsRootSpan confirms a wrapper persisted
// without trace fields (a producer with no trace, or a row written before
// trace ids were persisted) decodes cleanly and runs the job as a root span:
// a fresh trace, a fresh span and no parent.
func TestWorker_PayloadWithoutTraceStartsRootSpan(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	job := &traceCapturingJob{
		ID: "legacy-1",
		captured: make(chan struct {
			traceID  string
			spanID   string
			parentID string
		}, 1),
	}

	if err := q.PushCtx(context.Background(), job, "legacy-queue"); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	worker := NewWorker(q, "legacy-queue", func(j Job) error { return nil })
	worker.Start(context.Background())
	defer worker.Stop(context.Background())

	got := waitForTrace(t, job.captured, 5*time.Second)
	if got.traceID == "" || got.spanID == "" || got.parentID != "" {
		t.Errorf("want a root span, got trace=%q span=%q parent=%q", got.traceID, got.spanID, got.parentID)
	}
}

// plainDriver exposes only the base Driver methods, hiding the memory
// driver's TraceAwareDriver and ReservationDriver capabilities so the worker
// takes the bare PopCtx path a driver without trace support takes.
type plainDriver struct{ Driver }

// TestWorker_DriverWithoutTraceSupportStartsRootSpan covers the fallback the
// package doc promises: a driver that does not persist trace ids pops through
// PopCtx and the job runs as a root span, even when the worker's own context
// carries a trace.
func TestWorker_DriverWithoutTraceSupportStartsRootSpan(t *testing.T) {
	mem := NewMemoryDriver()
	mem.Start()
	defer mem.Shutdown(context.Background())
	q := plainDriver{Driver: mem}

	job := &traceCapturingJob{
		ID: "plain-1",
		captured: make(chan struct {
			traceID  string
			spanID   string
			parentID string
		}, 1),
	}

	producerCtx := trace.WithTrace(context.Background(), "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7")
	if err := q.PushCtx(producerCtx, job, "plain-queue"); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	workerCtx := trace.WithTrace(context.Background(), "0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331")
	worker := NewWorker(q, "plain-queue", func(j Job) error { return nil })
	worker.Start(workerCtx)
	defer worker.Stop(context.Background())

	got := waitForTrace(t, job.captured, 5*time.Second)
	if got.traceID == "" || got.spanID == "" || got.parentID != "" {
		t.Errorf("want a root span, got trace=%q span=%q parent=%q", got.traceID, got.spanID, got.parentID)
	}
	if got.traceID == "4bf92f3577b34da6a3ce929d0e0e4736" || got.traceID == "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace id %q leaked from a context the driver cannot carry", got.traceID)
	}
}

// traceOnlyDriver exposes the base Driver methods plus PopCtxWithTrace,
// hiding ReservationDriver: the shape of a driver that deletes on pop (redis)
// and therefore retries a failed job by pushing a fresh copy.
type traceOnlyDriver struct {
	Driver
	traced TraceAwareDriver
}

func (d traceOnlyDriver) PopCtxWithTrace(ctx context.Context, queueName string) (Job, TraceContext, error) {
	return d.traced.PopCtxWithTrace(ctx, queueName)
}

// flakyTraceJob fails its first attempt and records the trace ids of every
// attempt.
type flakyTraceJob struct {
	ID       string
	attempts atomic.Int32
	captured chan struct {
		traceID  string
		spanID   string
		parentID string
	}
}

func (j *flakyTraceJob) Handle() error { return nil }
func (j *flakyTraceJob) HandleCtx(ctx context.Context) error {
	t, s, p := trace.GetTraceContext(ctx)
	j.captured <- struct {
		traceID  string
		spanID   string
		parentID string
	}{t, s, p}
	if j.attempts.Add(1) == 1 {
		return errFlakyFirstAttempt
	}
	return nil
}
func (j *flakyTraceJob) Failed(err error)         {}
func (j *flakyTraceJob) JobID() string            { return j.ID }
func (j *flakyTraceJob) Backoff() []time.Duration { return []time.Duration{time.Millisecond} }

var errFlakyFirstAttempt = errors.New("first attempt fails")

// TestWorker_RetryPushKeepsProducerSpanAsParent covers a retry on a driver
// that re-pushes the failed job: every attempt runs as a new span under the
// producer's span, as it does on the reservation drivers that release the
// same row.
func TestWorker_RetryPushKeepsProducerSpanAsParent(t *testing.T) {
	mem := NewMemoryDriver()
	mem.Start()
	defer mem.Shutdown(context.Background())
	q := traceOnlyDriver{Driver: mem, traced: mem}

	job := &flakyTraceJob{
		ID: "flaky-1",
		captured: make(chan struct {
			traceID  string
			spanID   string
			parentID string
		}, 4),
	}

	producerTrace := "4bf92f3577b34da6a3ce929d0e0e4736"
	producerSpan := "00f067aa0ba902b7"
	producerCtx := trace.WithTrace(context.Background(), producerTrace, producerSpan)
	if err := q.PushCtx(producerCtx, job, "flaky-queue"); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	worker := NewWorker(q, "flaky-queue", func(j Job) error { return nil })
	worker.Start(context.Background())
	defer worker.Stop(context.Background())

	first := waitForTrace(t, job.captured, 5*time.Second)
	second := waitForTrace(t, job.captured, 5*time.Second)
	for i, got := range []struct {
		traceID  string
		spanID   string
		parentID string
	}{first, second} {
		if got.traceID != producerTrace {
			t.Errorf("attempt %d trace id: got %q want the producer's %q", i+1, got.traceID, producerTrace)
		}
		if got.parentID != producerSpan {
			t.Errorf("attempt %d parent id: got %q want the producer's span %q", i+1, got.parentID, producerSpan)
		}
	}
	if first.spanID == second.spanID {
		t.Errorf("both attempts ran in span %q, want a new span per attempt", first.spanID)
	}
}

// TestWorker_JobProcessingIsChildOfProducerSpan pins the event half:
// JobProcessing carries the producer's trace id, a new span id (the span the
// job runs in) and the producer's span as ParentID.
func TestWorker_JobProcessingIsChildOfProducerSpan(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	var (
		mu         sync.Mutex
		processing *JobProcessing
	)
	dispatcher := func(ctx context.Context, event interface{}) error {
		if e, ok := event.(*JobProcessing); ok {
			mu.Lock()
			processing = e
			mu.Unlock()
		}
		return nil
	}

	job := &traceCapturingJob{
		ID: "processing-1",
		captured: make(chan struct {
			traceID  string
			spanID   string
			parentID string
		}, 1),
	}
	producerTrace := "4bf92f3577b34da6a3ce929d0e0e4736"
	producerSpan := "00f067aa0ba902b7"
	producerCtx := trace.WithTrace(context.Background(), producerTrace, producerSpan)
	if err := q.PushCtx(producerCtx, job, "processing-queue"); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	worker := NewWorker(q, "processing-queue", func(j Job) error { return nil })
	worker.SetEventDispatcher(dispatcher)
	worker.Start(context.Background())
	defer worker.Stop(context.Background())

	got := waitForTrace(t, job.captured, 5*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if processing == nil {
		t.Fatal("JobProcessing not dispatched")
	}
	if processing.TraceID != producerTrace {
		t.Errorf("JobProcessing.TraceID = %q, want the producer's %q", processing.TraceID, producerTrace)
	}
	if processing.SpanID == "" || processing.SpanID == producerSpan {
		t.Errorf("JobProcessing.SpanID = %q, want a new span (producer span %q)", processing.SpanID, producerSpan)
	}
	if processing.ParentID != producerSpan {
		t.Errorf("JobProcessing.ParentID = %q, want the producer's span %q", processing.ParentID, producerSpan)
	}
	if got.spanID != processing.SpanID {
		t.Errorf("job ran in span %q, JobProcessing reported %q", got.spanID, processing.SpanID)
	}
}

// TestWorker_JobProcessedEventsCarryProducerTrace confirms downstream events
// (JobProcessing / JobProcessed) read the producer trace ids out of the
// per-job ctx restored from the wrapper.
func TestWorker_JobProcessedEventsCarryProducerTrace(t *testing.T) {
	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	var (
		mu     sync.Mutex
		events []interface{}
	)
	dispatcher := func(ctx context.Context, event interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		return nil
	}
	q.SetEventDispatcher(dispatcher)

	producerTrace := "events123456789012345678901234ab"
	producerSpan := "espan1234567890a"
	producerCtx := trace.WithTrace(context.Background(), producerTrace, producerSpan)

	processed := int32(0)
	worker := NewWorker(q, "event-queue", func(j Job) error {
		atomic.AddInt32(&processed, 1)
		return nil
	})
	worker.SetEventDispatcher(dispatcher)

	if err := q.PushCtx(producerCtx, &TestJob{ID: "ev-1", Message: "ok"}, "event-queue"); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	worker.Start(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&processed) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	worker.Stop(context.Background())

	if atomic.LoadInt32(&processed) == 0 {
		t.Fatal("job not processed")
	}

	mu.Lock()
	defer mu.Unlock()

	var sawProcessing, sawProcessed bool
	for _, ev := range events {
		switch e := ev.(type) {
		case *JobProcessing:
			sawProcessing = true
			if e.TraceID != producerTrace {
				t.Errorf("JobProcessing TraceID: got %q want %q", e.TraceID, producerTrace)
			}
		case *JobProcessed:
			sawProcessed = true
			if e.TraceID != producerTrace {
				t.Errorf("JobProcessed TraceID: got %q want %q", e.TraceID, producerTrace)
			}
		}
	}
	if !sawProcessing || !sawProcessed {
		t.Errorf("missing trace-stamped events: processing=%v processed=%v", sawProcessing, sawProcessed)
	}
}
