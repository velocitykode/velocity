package queue

import (
	"context"
	"errors"
	"github.com/velocitykode/velocity/contract"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/eventemit/eventemittest"
	testsync "github.com/velocitykode/velocity/testing"
	"github.com/velocitykode/velocity/trace"
)

func TestEventNames(t *testing.T) {
	tests := []struct {
		name     string
		event    interface{ Name() string }
		expected string
	}{
		{"JobQueued", &JobQueued{}, "queue.job.queued"},
		{"JobProcessing", &JobProcessing{}, "queue.job.started"},
		{"JobProcessed", &JobProcessed{}, "queue.job.completed"},
		{"JobFailed", &JobFailed{}, "queue.job.failed"},
		{"JobRetrying", &JobRetrying{}, "queue.job.retried"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.Name(); got != tt.expected {
				t.Errorf("Name() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestDispatcher(t *testing.T) {
	t.Run("SetEventDispatcher on MemoryDriver", func(t *testing.T) {
		q := NewMemoryDriver()
		q.Start()
		defer q.Shutdown(context.Background())

		q.SetEventDispatcher(nil)

		called := false
		q.SetEventDispatcher(func(_ context.Context, event interface{}) error {
			called = true
			return nil
		})

		// Dispatch an event by pushing a job
		job := &TestJob{ID: "test", Message: "test"}
		_ = q.PushCtx(context.Background(), job, "test-queue")

		if !called {
			t.Error("dispatcher was not called")
		}

		q.SetEventDispatcher(nil)
	})

	t.Run("DispatchJobQueued with nil dispatcher", func(t *testing.T) {
		q := NewMemoryDriver()
		q.Start()
		defer q.Shutdown(context.Background())
		q.SetEventDispatcher(nil)

		// Should not panic - dispatch function checks for nil
		q.DispatchJobQueued(context.Background(), "t", "default", false, 0)
	})

	t.Run("DispatchJobQueued with error returning dispatcher", func(t *testing.T) {
		q := NewMemoryDriver()
		q.Start()
		defer q.Shutdown(context.Background())
		q.SetEventDispatcher(func(_ context.Context, event interface{}) error {
			return errors.New("dispatcher error")
		})

		// Should not panic, errors are ignored
		q.DispatchJobQueued(context.Background(), "t", "default", false, 0)

		q.SetEventDispatcher(nil)
	})
}

func TestDispatchJobQueued(t *testing.T) {
	var captured *JobQueued
	dispatch := func(_ context.Context, event interface{}) {
		if e, ok := event.(*JobQueued); ok {
			captured = e
		}
	}

	t.Run("immediate job", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		dispatchJobQueued(eventemittest.Receiving(dispatch), ctx, "*queue.TestJob", "default", false, 0)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.JobType != "*queue.TestJob" {
			t.Errorf("JobType = %q, want %q", captured.JobType, "*queue.TestJob")
		}
		if captured.Queue != "default" {
			t.Errorf("Queue = %q, want %q", captured.Queue, "default")
		}
		if captured.Delayed {
			t.Error("Delayed should be false for immediate job")
		}
		if captured.Delay != 0*time.Millisecond {
			t.Errorf("Delay = %v, want 0ms", captured.Delay)
		}
	})

	t.Run("delayed job", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		delay := 5 * time.Second
		dispatchJobQueued(eventemittest.Receiving(dispatch), ctx, "*queue.EmailJob", "emails", true, delay)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.JobType != "*queue.EmailJob" {
			t.Errorf("JobType = %q, want %q", captured.JobType, "*queue.EmailJob")
		}
		if captured.Queue != "emails" {
			t.Errorf("Queue = %q, want %q", captured.Queue, "emails")
		}
		if !captured.Delayed {
			t.Error("Delayed should be true for delayed job")
		}
		if captured.Delay != 5000*time.Millisecond {
			t.Errorf("Delay = %v, want 5000ms", captured.Delay)
		}
	})

	t.Run("with trace context", func(t *testing.T) {
		captured = nil
		// Start with trace and first span, then create child span (which sets parent)
		ctx := trace.WithTrace(context.Background(), "trace-123", "parent-span")
		ctx = trace.WithSpan(ctx, "span-456")
		dispatchJobQueued(eventemittest.Receiving(dispatch), ctx, "*queue.TestJob", "default", false, 0)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.TraceID != "trace-123" {
			t.Errorf("TraceID = %q, want %q", captured.TraceID, "trace-123")
		}
		if captured.SpanID != "span-456" {
			t.Errorf("SpanID = %q, want %q", captured.SpanID, "span-456")
		}
		if captured.ParentID != "parent-span" {
			t.Errorf("ParentID = %q, want %q", captured.ParentID, "parent-span")
		}
	})

	t.Run("with nil dispatch", func(t *testing.T) {
		// Should not panic
		dispatchJobQueued(nil, context.Background(), "*queue.TestJob", "default", false, 0)
	})
}

func TestDispatchJobProcessing(t *testing.T) {
	var captured *JobProcessing
	dispatch := func(_ context.Context, event interface{}) {
		if e, ok := event.(*JobProcessing); ok {
			captured = e
		}
	}

	t.Run("basic dispatch", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		dispatchJobProcessing(eventemittest.Receiving(dispatch), ctx, "*queue.TestJob", "default")

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.JobType != "*queue.TestJob" {
			t.Errorf("JobType = %q, want %q", captured.JobType, "*queue.TestJob")
		}
		if captured.Queue != "default" {
			t.Errorf("Queue = %q, want %q", captured.Queue, "default")
		}
	})

	t.Run("with trace context", func(t *testing.T) {
		captured = nil
		ctx := trace.WithTrace(context.Background(), "trace-abc", "parent-ghi")
		ctx = trace.WithSpan(ctx, "span-def")
		dispatchJobProcessing(eventemittest.Receiving(dispatch), ctx, "*queue.TestJob", "high-priority")

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.TraceID != "trace-abc" {
			t.Errorf("TraceID = %q, want %q", captured.TraceID, "trace-abc")
		}
		if captured.SpanID != "span-def" {
			t.Errorf("SpanID = %q, want %q", captured.SpanID, "span-def")
		}
		if captured.ParentID != "parent-ghi" {
			t.Errorf("ParentID = %q, want %q", captured.ParentID, "parent-ghi")
		}
	})
}

func TestDispatchJobProcessed(t *testing.T) {
	var captured *JobProcessed
	dispatch := func(_ context.Context, event interface{}) {
		if e, ok := event.(*JobProcessed); ok {
			captured = e
		}
	}

	t.Run("basic dispatch", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		duration := 150 * time.Millisecond
		dispatchJobProcessed(eventemittest.Receiving(dispatch), ctx, "*queue.TestJob", "default", duration)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.JobType != "*queue.TestJob" {
			t.Errorf("JobType = %q, want %q", captured.JobType, "*queue.TestJob")
		}
		if captured.Queue != "default" {
			t.Errorf("Queue = %q, want %q", captured.Queue, "default")
		}
		if captured.Duration != 150*time.Millisecond {
			t.Errorf("Duration = %v, want 150ms", captured.Duration)
		}
	})

	t.Run("with trace context", func(t *testing.T) {
		captured = nil
		ctx := trace.WithTrace(context.Background(), "trace-xyz", "span-uvw")
		dispatchJobProcessed(eventemittest.Receiving(dispatch), ctx, "*queue.ReportJob", "reports", 2*time.Second)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.TraceID != "trace-xyz" {
			t.Errorf("TraceID = %q, want %q", captured.TraceID, "trace-xyz")
		}
		if captured.SpanID != "span-uvw" {
			t.Errorf("SpanID = %q, want %q", captured.SpanID, "span-uvw")
		}
		if captured.Duration != 2000*time.Millisecond {
			t.Errorf("Duration = %v, want 2000ms", captured.Duration)
		}
	})
}

func TestDispatchJobFailed(t *testing.T) {
	var captured *JobFailed
	dispatch := func(_ context.Context, event interface{}) {
		if e, ok := event.(*JobFailed); ok {
			captured = e
		}
	}

	t.Run("basic dispatch", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		err := errors.New("connection refused")
		duration := 50 * time.Millisecond
		dispatchJobFailed(eventemittest.Receiving(dispatch), ctx, "*queue.TestJob", "default", "", err, duration)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.JobType != "*queue.TestJob" {
			t.Errorf("JobType = %q, want %q", captured.JobType, "*queue.TestJob")
		}
		if captured.Queue != "default" {
			t.Errorf("Queue = %q, want %q", captured.Queue, "default")
		}
		if captured.Err == nil || captured.Err.Error() != "connection refused" {
			t.Errorf("Err = %v, want %q", captured.Err, "connection refused")
		}
		if captured.Duration != 50*time.Millisecond {
			t.Errorf("Duration = %v, want 50ms", captured.Duration)
		}
	})

	t.Run("with nil error", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		dispatchJobFailed(eventemittest.Receiving(dispatch), ctx, "*queue.TestJob", "default", "", nil, 100*time.Millisecond)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.Err != nil {
			t.Errorf("Err = %v, want nil", captured.Err)
		}
	})

	t.Run("with trace context", func(t *testing.T) {
		captured = nil
		ctx := trace.WithTrace(context.Background(), "trace-fail", "parent-fail")
		ctx = trace.WithSpan(ctx, "span-fail")
		dispatchJobFailed(eventemittest.Receiving(dispatch), ctx, "*queue.NotificationJob", "notifications", "", errors.New("timeout"), 5*time.Second)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.TraceID != "trace-fail" {
			t.Errorf("TraceID = %q, want %q", captured.TraceID, "trace-fail")
		}
		if captured.SpanID != "span-fail" {
			t.Errorf("SpanID = %q, want %q", captured.SpanID, "span-fail")
		}
		if captured.ParentID != "parent-fail" {
			t.Errorf("ParentID = %q, want %q", captured.ParentID, "parent-fail")
		}
	})
}

func TestEventDispatchingIntegration(t *testing.T) {
	// Test that events are actually dispatched during queue operations
	var events []interface{}
	var mu sync.Mutex

	q := NewMemoryDriver()
	q.Start()
	defer q.Shutdown(context.Background())

	q.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	})

	t.Run("Push dispatches JobQueued", func(t *testing.T) {
		events = nil
		job := &TestJob{ID: "test-1", Message: "test"}

		err := q.PushCtx(context.Background(), job, "test-queue")
		if err != nil {
			t.Fatalf("Push failed: %v", err)
		}

		mu.Lock()
		defer mu.Unlock()

		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}

		queued, ok := events[0].(*JobQueued)
		if !ok {
			t.Fatalf("expected *JobQueued, got %T", events[0])
		}
		if queued.Queue != "test-queue" {
			t.Errorf("Queue = %q, want %q", queued.Queue, "test-queue")
		}
		if queued.Delayed {
			t.Error("Delayed should be false")
		}
	})

	t.Run("PushDelayed dispatches JobQueued with delay info", func(t *testing.T) {
		mu.Lock()
		events = nil
		mu.Unlock()

		job := &TestJob{ID: "test-2", Message: "delayed test"}

		err := q.PushDelayedCtx(context.Background(), job, 2*time.Second, "delayed-queue")
		if err != nil {
			t.Fatalf("PushDelayed failed: %v", err)
		}

		mu.Lock()
		defer mu.Unlock()

		if len(events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(events))
		}

		queued, ok := events[0].(*JobQueued)
		if !ok {
			t.Fatalf("expected *JobQueued, got %T", events[0])
		}
		if queued.Queue != "delayed-queue" {
			t.Errorf("Queue = %q, want %q", queued.Queue, "delayed-queue")
		}
		if !queued.Delayed {
			t.Error("Delayed should be true")
		}
		if queued.Delay != 2000*time.Millisecond {
			t.Errorf("Delay = %v, want 2000ms", queued.Delay)
		}
	})
}

func TestWorkerEventDispatching(t *testing.T) {
	var processingEvents []*JobProcessing
	var processedEvents []*JobProcessed
	var failedEvents []*JobFailed
	var retryingEvents []*JobRetrying
	var mu sync.Mutex

	dispatcher := func(_ context.Context, event interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		switch e := event.(type) {
		case *JobProcessing:
			processingEvents = append(processingEvents, e)
		case *JobProcessed:
			processedEvents = append(processedEvents, e)
		case *JobFailed:
			failedEvents = append(failedEvents, e)
		case *JobRetrying:
			retryingEvents = append(retryingEvents, e)
		}
		return nil
	}

	t.Run("successful job dispatches processing and processed", func(t *testing.T) {
		processingEvents = nil
		processedEvents = nil
		failedEvents = nil
		retryingEvents = nil

		q := NewMemoryDriver()
		q.Start()
		defer q.Shutdown(context.Background())
		q.SetEventDispatcher(dispatcher)

		processed := int32(0)
		job := &TestJob{
			ID:      "success-job",
			Message: "test",
			Handler: func() error {
				atomic.AddInt32(&processed, 1)
				time.Sleep(10 * time.Millisecond) // Simulate work
				return nil
			},
		}

		err := q.PushCtx(context.Background(), job, "success-queue")
		if err != nil {
			t.Fatalf("Push failed: %v", err)
		}

		worker := NewWorker(q, "success-queue", func(j Job) error {
			return j.Handle()
		}, WithInterval(10*time.Millisecond))
		worker.SetEventDispatcher(dispatcher)

		worker.Start(context.Background())
		defer worker.Stop(context.Background())

		testsync.Eventually(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(processedEvents) == 1
		}, 2*time.Second, "success job processed event")

		mu.Lock()
		defer mu.Unlock()

		if len(processingEvents) != 1 {
			t.Errorf("expected 1 processing event, got %d", len(processingEvents))
		}
		if len(processedEvents) != 1 {
			t.Errorf("expected 1 processed event, got %d", len(processedEvents))
		}
		if len(failedEvents) != 0 {
			t.Errorf("expected 0 failed events, got %d", len(failedEvents))
		}

		if len(processedEvents) > 0 && processedEvents[0].Duration < 10*time.Millisecond {
			t.Errorf("Duration should be at least 10ms, got %v", processedEvents[0].Duration)
		}
	})

	t.Run("failed job dispatches processing and failed", func(t *testing.T) {
		mu.Lock()
		processingEvents = nil
		processedEvents = nil
		failedEvents = nil
		retryingEvents = nil
		mu.Unlock()

		q := NewMemoryDriver()
		q.Start()
		defer q.Shutdown(context.Background())
		q.SetEventDispatcher(dispatcher)

		job := &TestJob{
			ID:      "fail-job",
			Message: "test",
			Handler: func() error {
				return errors.New("intentional failure")
			},
		}

		err := q.PushCtx(context.Background(), job, "fail-queue")
		if err != nil {
			t.Fatalf("Push failed: %v", err)
		}

		worker := NewWorker(q, "fail-queue", func(j Job) error {
			return j.Handle()
		}, WithInterval(10*time.Millisecond), WithMaxRetries(1))
		worker.SetEventDispatcher(dispatcher)

		worker.Start(context.Background())
		defer worker.Stop(context.Background())

		testsync.Eventually(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(failedEvents) == 1
		}, 2*time.Second, "failed job dispatches failed event")

		mu.Lock()
		defer mu.Unlock()

		if len(processingEvents) != 1 {
			t.Errorf("expected 1 processing event, got %d", len(processingEvents))
		}
		if len(processedEvents) != 0 {
			t.Errorf("expected 0 processed events, got %d", len(processedEvents))
		}
		if len(failedEvents) != 1 {
			t.Errorf("expected 1 failed event, got %d", len(failedEvents))
		}

		if len(failedEvents) > 0 && (failedEvents[0].Err == nil || failedEvents[0].Err.Error() != "intentional failure") {
			t.Errorf("Err = %v, want %q", failedEvents[0].Err, "intentional failure")
		}
	})
}

func TestJobQueuedEventFields(t *testing.T) {
	e := &JobQueued{
		EventMeta: contract.EventMeta{Context: context.Background(), TraceID: "trace-123", SpanID: "span-456", ParentID: "parent-789"},
		JobType:   "*queue.EmailJob",
		Queue:     "emails",
		Delayed:   true,
		Delay:     5000 * time.Millisecond,
	}

	if e.Name() != "queue.job.queued" {
		t.Errorf("Name() = %q, want %q", e.Name(), "queue.job.queued")
	}
	if e.JobType != "*queue.EmailJob" {
		t.Errorf("JobType = %q, want %q", e.JobType, "*queue.EmailJob")
	}
	if e.Queue != "emails" {
		t.Errorf("Queue = %q, want %q", e.Queue, "emails")
	}
	if !e.Delayed {
		t.Error("Delayed should be true")
	}
	if e.Delay != 5000*time.Millisecond {
		t.Errorf("Delay = %v, want 5000ms", e.Delay)
	}
	if e.TraceID != "trace-123" {
		t.Errorf("TraceID = %q, want %q", e.TraceID, "trace-123")
	}
	if e.SpanID != "span-456" {
		t.Errorf("SpanID = %q, want %q", e.SpanID, "span-456")
	}
	if e.ParentID != "parent-789" {
		t.Errorf("ParentID = %q, want %q", e.ParentID, "parent-789")
	}
}

func TestJobProcessingEventFields(t *testing.T) {
	e := &JobProcessing{
		EventMeta: contract.EventMeta{Context: context.Background(), TraceID: "trace-abc", SpanID: "span-def", ParentID: "parent-ghi"},
		JobType:   "*queue.ReportJob",
		Queue:     "reports",
	}

	if e.Name() != "queue.job.started" {
		t.Errorf("Name() = %q, want %q", e.Name(), "queue.job.started")
	}
	if e.JobType != "*queue.ReportJob" {
		t.Errorf("JobType = %q, want %q", e.JobType, "*queue.ReportJob")
	}
	if e.Queue != "reports" {
		t.Errorf("Queue = %q, want %q", e.Queue, "reports")
	}
}

func TestJobProcessedEventFields(t *testing.T) {
	e := &JobProcessed{
		EventMeta: contract.EventMeta{Context: context.Background(), TraceID: "trace-xyz", SpanID: "span-uvw", ParentID: ""},
		JobType:   "*queue.NotificationJob",
		Queue:     "notifications",
		Duration:  1500 * time.Millisecond,
	}

	if e.Name() != "queue.job.completed" {
		t.Errorf("Name() = %q, want %q", e.Name(), "queue.job.completed")
	}
	if e.Duration != 1500*time.Millisecond {
		t.Errorf("Duration = %v, want 1500ms", e.Duration)
	}
}

func TestJobFailedEventFields(t *testing.T) {
	e := &JobFailed{
		EventMeta: contract.EventMeta{Context: context.Background(), TraceID: "trace-fail", SpanID: "span-fail", ParentID: "parent-fail"},
		JobType:   "*queue.PaymentJob",
		Queue:     "payments",
		Err:       errors.New("payment gateway timeout"),
		Duration:  30000 * time.Millisecond,
	}

	if e.Name() != "queue.job.failed" {
		t.Errorf("Name() = %q, want %q", e.Name(), "queue.job.failed")
	}
	if e.Err == nil || e.Err.Error() != "payment gateway timeout" {
		t.Errorf("Err = %v, want %q", e.Err, "payment gateway timeout")
	}
	if e.Duration != 30000*time.Millisecond {
		t.Errorf("Duration = %v, want 30000ms", e.Duration)
	}
}

func TestJobRetryingEventName(t *testing.T) {
	e := &JobRetrying{
		EventMeta:   contract.EventMeta{Context: context.Background(), TraceID: "trace-retry", SpanID: "span-retry", ParentID: "parent-retry"},
		JobType:     "*queue.TestJob",
		Queue:       "default",
		Attempt:     2,
		MaxAttempts: 5,
		Err:         errors.New("connection refused"),
		Backoff:     2000 * time.Millisecond,
	}

	if e.Name() != "queue.job.retried" {
		t.Errorf("Name() = %q, want %q", e.Name(), "queue.job.retried")
	}
	if e.Attempt != 2 {
		t.Errorf("Attempt = %d, want 2", e.Attempt)
	}
	if e.MaxAttempts != 5 {
		t.Errorf("MaxAttempts = %d, want 5", e.MaxAttempts)
	}
	if e.Err == nil || e.Err.Error() != "connection refused" {
		t.Errorf("Err = %v, want %q", e.Err, "connection refused")
	}
	if e.Backoff != 2000*time.Millisecond {
		t.Errorf("Backoff = %v, want 2000ms", e.Backoff)
	}
}

func TestDispatchJobRetrying(t *testing.T) {
	var captured *JobRetrying
	dispatch := func(_ context.Context, event interface{}) {
		if e, ok := event.(*JobRetrying); ok {
			captured = e
		}
	}

	t.Run("basic dispatch", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		err := errors.New("timeout")
		dispatchJobRetrying(eventemittest.Receiving(dispatch), ctx, "*queue.TestJob", "default", 2, 5, err, 4*time.Second)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.Attempt != 2 {
			t.Errorf("Attempt = %d, want 2", captured.Attempt)
		}
		if captured.MaxAttempts != 5 {
			t.Errorf("MaxAttempts = %d, want 5", captured.MaxAttempts)
		}
		if captured.Err == nil || captured.Err.Error() != "timeout" {
			t.Errorf("Err = %v, want %q", captured.Err, "timeout")
		}
		if captured.Backoff != 4000*time.Millisecond {
			t.Errorf("Backoff = %v, want 4000ms", captured.Backoff)
		}
	})

	t.Run("with nil dispatch", func(t *testing.T) {
		// Should not panic
		dispatchJobRetrying(nil, context.Background(), "*queue.TestJob", "default", 1, 3, errors.New("err"), time.Second)
	})

	t.Run("with nil error", func(t *testing.T) {
		captured = nil
		dispatchJobRetrying(eventemittest.Receiving(dispatch), context.Background(), "*queue.TestJob", "default", 1, 3, nil, time.Second)
		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.Err != nil {
			t.Errorf("Err = %v, want nil", captured.Err)
		}
	})
}
