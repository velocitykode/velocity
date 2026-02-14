package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/velocitykode/velocity/trace"
)

func TestSchedulerEventNames(t *testing.T) {
	tests := []struct {
		name     string
		event    interface{ Name() string }
		expected string
	}{
		{"ScheduledTaskStarting", &ScheduledTaskStarting{}, "scheduled.starting"},
		{"ScheduledTaskFinished", &ScheduledTaskFinished{}, "scheduled.finished"},
		{"ScheduledTaskFailed", &ScheduledTaskFailed{}, "scheduled.failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.Name(); got != tt.expected {
				t.Errorf("Name() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestSchedulerDispatcher(t *testing.T) {
	t.Run("SetEventDispatcher", func(t *testing.T) {
		s := New()
		s.SetEventDispatcher(nil)

		called := false
		s.SetEventDispatcher(func(event interface{}) error {
			called = true
			return nil
		})

		s.dispatchEvent(&ScheduledTaskStarting{})

		if !called {
			t.Error("dispatcher was not called")
		}

		s.SetEventDispatcher(nil)
	})

	t.Run("dispatchEvent with nil dispatcher", func(t *testing.T) {
		s := New()
		s.SetEventDispatcher(nil)
		// Should not panic
		s.dispatchEvent(&ScheduledTaskStarting{})
	})

	t.Run("dispatchEvent with error returning dispatcher", func(t *testing.T) {
		s := New()
		s.SetEventDispatcher(func(event interface{}) error {
			return errors.New("dispatcher error")
		})

		// Should not panic
		s.dispatchEvent(&ScheduledTaskStarting{})

		s.SetEventDispatcher(nil)
	})
}

func TestDispatchScheduledTaskStarting(t *testing.T) {
	var captured *ScheduledTaskStarting
	dispatch := func(event interface{}) {
		if e, ok := event.(*ScheduledTaskStarting); ok {
			captured = e
		}
	}

	t.Run("basic dispatch", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		dispatchScheduledTaskStarting(dispatch, ctx, "cleanup-old-files")

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.TaskName != "cleanup-old-files" {
			t.Errorf("Name = %q, want %q", captured.TaskName, "cleanup-old-files")
		}
	})

	t.Run("with trace context", func(t *testing.T) {
		captured = nil
		ctx := trace.WithTrace(context.Background(), "trace-sched", "parent-sched")
		ctx = trace.WithSpan(ctx, "span-sched")
		dispatchScheduledTaskStarting(dispatch, ctx, "send-reports")

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.TraceID != "trace-sched" {
			t.Errorf("TraceID = %q, want %q", captured.TraceID, "trace-sched")
		}
		if captured.SpanID != "span-sched" {
			t.Errorf("SpanID = %q, want %q", captured.SpanID, "span-sched")
		}
		if captured.ParentID != "parent-sched" {
			t.Errorf("ParentID = %q, want %q", captured.ParentID, "parent-sched")
		}
	})

	t.Run("with nil dispatch", func(t *testing.T) {
		// Should not panic
		dispatchScheduledTaskStarting(nil, context.Background(), "test")
	})
}

func TestDispatchScheduledTaskFinished(t *testing.T) {
	var captured *ScheduledTaskFinished
	dispatch := func(event interface{}) {
		if e, ok := event.(*ScheduledTaskFinished); ok {
			captured = e
		}
	}

	t.Run("basic dispatch", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		dispatchScheduledTaskFinished(dispatch, ctx, "backup-database", 5*time.Second)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.TaskName != "backup-database" {
			t.Errorf("Name = %q, want %q", captured.TaskName, "backup-database")
		}
		if captured.DurationMs != 5000 {
			t.Errorf("DurationMs = %d, want 5000", captured.DurationMs)
		}
	})

	t.Run("with trace context", func(t *testing.T) {
		captured = nil
		ctx := trace.WithTrace(context.Background(), "trace-done", "parent-done")
		ctx = trace.WithSpan(ctx, "span-done")
		dispatchScheduledTaskFinished(dispatch, ctx, "sync-data", 2*time.Second)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.TraceID != "trace-done" {
			t.Errorf("TraceID = %q, want %q", captured.TraceID, "trace-done")
		}
		if captured.SpanID != "span-done" {
			t.Errorf("SpanID = %q, want %q", captured.SpanID, "span-done")
		}
	})
}

func TestDispatchScheduledTaskFailed(t *testing.T) {
	var captured *ScheduledTaskFailed
	dispatch := func(event interface{}) {
		if e, ok := event.(*ScheduledTaskFailed); ok {
			captured = e
		}
	}

	t.Run("basic dispatch", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		err := errors.New("disk full")
		dispatchScheduledTaskFailed(dispatch, ctx, "cleanup", err, 10*time.Second)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.TaskName != "cleanup" {
			t.Errorf("Name = %q, want %q", captured.TaskName, "cleanup")
		}
		if captured.Error != "disk full" {
			t.Errorf("Error = %q, want %q", captured.Error, "disk full")
		}
		if captured.DurationMs != 10000 {
			t.Errorf("DurationMs = %d, want 10000", captured.DurationMs)
		}
	})

	t.Run("with nil error", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		dispatchScheduledTaskFailed(dispatch, ctx, "task", nil, 100*time.Millisecond)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.Error != "" {
			t.Errorf("Error = %q, want empty string", captured.Error)
		}
	})

	t.Run("with trace context", func(t *testing.T) {
		captured = nil
		ctx := trace.WithTrace(context.Background(), "trace-fail", "parent-fail")
		ctx = trace.WithSpan(ctx, "span-fail")
		dispatchScheduledTaskFailed(dispatch, ctx, "email-report", errors.New("smtp error"), 30*time.Second)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.TraceID != "trace-fail" {
			t.Errorf("TraceID = %q, want %q", captured.TraceID, "trace-fail")
		}
		if captured.SpanID != "span-fail" {
			t.Errorf("SpanID = %q, want %q", captured.SpanID, "span-fail")
		}
	})

	t.Run("with nil dispatch", func(t *testing.T) {
		// Should not panic
		dispatchScheduledTaskFailed(nil, context.Background(), "task", nil, 100*time.Millisecond)
	})
}

func TestScheduledTaskStartingEventFields(t *testing.T) {
	e := &ScheduledTaskStarting{
		Context:  context.Background(),
		TaskName: "daily-backup",
		TraceID:  "trace-123",
		SpanID:   "span-456",
		ParentID: "parent-789",
	}

	if e.Name() != "scheduled.starting" {
		t.Errorf("Name() = %q, want %q", e.Name(), "scheduled.starting")
	}
	if e.TaskName != "daily-backup" {
		t.Errorf("TaskName = %q, want %q", e.TaskName, "daily-backup")
	}
}

func TestScheduledTaskFinishedEventFields(t *testing.T) {
	e := &ScheduledTaskFinished{
		Context:    context.Background(),
		TaskName:   "hourly-sync",
		DurationMs: 1500,
		TraceID:    "trace-xyz",
		SpanID:     "span-abc",
		ParentID:   "",
	}

	if e.Name() != "scheduled.finished" {
		t.Errorf("Name() = %q, want %q", e.Name(), "scheduled.finished")
	}
	if e.DurationMs != 1500 {
		t.Errorf("DurationMs = %d, want 1500", e.DurationMs)
	}
}

func TestScheduledTaskFailedEventFields(t *testing.T) {
	e := &ScheduledTaskFailed{
		Context:    context.Background(),
		TaskName:   "monthly-report",
		Error:      "template error",
		DurationMs: 30000,
		TraceID:    "trace-err",
		SpanID:     "span-err",
		ParentID:   "parent-err",
	}

	if e.Name() != "scheduled.failed" {
		t.Errorf("Name() = %q, want %q", e.Name(), "scheduled.failed")
	}
	if e.Error != "template error" {
		t.Errorf("Error = %q, want %q", e.Error, "template error")
	}
}
