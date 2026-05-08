package queue

import (
	"context"
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
func (j *traceCapturingJob) Failed(err error)   {}
func (j *traceCapturingJob) JobID() string      { return j.ID }

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

// TestWorker_RestoresProducerTraceOnHandleCtx confirms PushCtx -> persist ->
// pop -> HandleCtx round-trips the producer's trace ids end-to-end. Uses the
// MemoryDriver because it implements TraceAwareDriver and exercises the same
// Payload field as the database / redis drivers.
func TestWorker_RestoresProducerTraceOnHandleCtx(t *testing.T) {
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

	producerTrace := "queue1234567890abcdef1234567890ab"
	producerSpan := "qspan1234567890a"
	producerCtx := trace.WithTrace(context.Background(), producerTrace, producerSpan)
	producerCtx = trace.WithSpan(producerCtx, "qchild1234567890")

	if err := q.PushCtx(producerCtx, job, "trace-queue"); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	worker := NewWorker(q, "trace-queue", func(j Job) error { return nil })
	worker.Start(context.Background())
	defer worker.Stop()

	got := waitForTrace(t, job.captured, 5*time.Second)
	if got.traceID != producerTrace {
		t.Errorf("trace id: got %q want %q", got.traceID, producerTrace)
	}
	wantSpan, wantParent := trace.GetSpanID(producerCtx), trace.GetParentID(producerCtx)
	if got.spanID != wantSpan {
		t.Errorf("span id: got %q want %q", got.spanID, wantSpan)
	}
	if got.parentID != wantParent {
		t.Errorf("parent id: got %q want %q", got.parentID, wantParent)
	}
}

// TestWorker_LegacyPayloadDecodesWithoutTrace confirms a wrapper persisted
// without trace fields decodes cleanly and runs the handler with an empty
// trace context (no panic, no spurious trace injection).
func TestWorker_LegacyPayloadDecodesWithoutTrace(t *testing.T) {
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
	defer worker.Stop()

	got := waitForTrace(t, job.captured, 5*time.Second)
	if got.traceID != "" || got.spanID != "" || got.parentID != "" {
		t.Errorf("expected empty trace ids, got trace=%q span=%q parent=%q", got.traceID, got.spanID, got.parentID)
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
	worker.Stop()

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
