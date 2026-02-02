package httpclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/velocitykode/velocity/pkg/trace"
)

func TestEventNames(t *testing.T) {
	tests := []struct {
		name     string
		event    interface{ Name() string }
		expected string
	}{
		{"RequestSent", &RequestSent{}, "http.request.sent"},
		{"RequestFailed", &RequestFailed{}, "http.request.failed"},
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
	t.Run("SetEventDispatcher", func(t *testing.T) {
		SetEventDispatcher(nil)

		called := false
		SetEventDispatcher(func(event interface{}) error {
			called = true
			return nil
		})

		dispatchEvent(&RequestSent{})

		if !called {
			t.Error("dispatcher was not called")
		}

		SetEventDispatcher(nil)
	})

	t.Run("dispatchEvent with nil dispatcher", func(t *testing.T) {
		SetEventDispatcher(nil)
		// Should not panic
		dispatchEvent(&RequestSent{})
	})

	t.Run("dispatchEvent with error returning dispatcher", func(t *testing.T) {
		SetEventDispatcher(func(event interface{}) error {
			return errors.New("dispatcher error")
		})

		// Should not panic
		dispatchEvent(&RequestSent{})

		SetEventDispatcher(nil)
	})
}

func TestDispatchRequestSent(t *testing.T) {
	var captured *RequestSent
	SetEventDispatcher(func(event interface{}) error {
		if e, ok := event.(*RequestSent); ok {
			captured = e
		}
		return nil
	})
	defer SetEventDispatcher(nil)

	t.Run("basic dispatch", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		dispatchRequestSent(ctx, "GET", "https://api.example.com/users", 200, 150*time.Millisecond, 0, 1024)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.Method != "GET" {
			t.Errorf("Method = %q, want %q", captured.Method, "GET")
		}
		if captured.URL != "https://api.example.com/users" {
			t.Errorf("URL = %q, want %q", captured.URL, "https://api.example.com/users")
		}
		if captured.StatusCode != 200 {
			t.Errorf("StatusCode = %d, want 200", captured.StatusCode)
		}
		if captured.DurationMs != 150 {
			t.Errorf("DurationMs = %d, want 150", captured.DurationMs)
		}
		if captured.RequestSize != 0 {
			t.Errorf("RequestSize = %d, want 0", captured.RequestSize)
		}
		if captured.ResponseSize != 1024 {
			t.Errorf("ResponseSize = %d, want 1024", captured.ResponseSize)
		}
	})

	t.Run("with trace context", func(t *testing.T) {
		captured = nil
		ctx := trace.WithTrace(context.Background(), "trace-http", "parent-span")
		ctx = trace.WithSpan(ctx, "span-http")
		dispatchRequestSent(ctx, "POST", "https://api.example.com/data", 201, 50*time.Millisecond, 512, 256)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.TraceID != "trace-http" {
			t.Errorf("TraceID = %q, want %q", captured.TraceID, "trace-http")
		}
		if captured.SpanID != "span-http" {
			t.Errorf("SpanID = %q, want %q", captured.SpanID, "span-http")
		}
		if captured.ParentID != "parent-span" {
			t.Errorf("ParentID = %q, want %q", captured.ParentID, "parent-span")
		}
	})
}

func TestDispatchRequestFailed(t *testing.T) {
	var captured *RequestFailed
	SetEventDispatcher(func(event interface{}) error {
		if e, ok := event.(*RequestFailed); ok {
			captured = e
		}
		return nil
	})
	defer SetEventDispatcher(nil)

	t.Run("basic dispatch", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		err := errors.New("connection refused")
		dispatchRequestFailed(ctx, "GET", "https://api.example.com/users", err, 5*time.Second)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.Method != "GET" {
			t.Errorf("Method = %q, want %q", captured.Method, "GET")
		}
		if captured.URL != "https://api.example.com/users" {
			t.Errorf("URL = %q, want %q", captured.URL, "https://api.example.com/users")
		}
		if captured.Error != "connection refused" {
			t.Errorf("Error = %q, want %q", captured.Error, "connection refused")
		}
		if captured.DurationMs != 5000 {
			t.Errorf("DurationMs = %d, want 5000", captured.DurationMs)
		}
	})

	t.Run("with nil error", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		dispatchRequestFailed(ctx, "GET", "https://api.example.com/users", nil, 100*time.Millisecond)

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
		dispatchRequestFailed(ctx, "POST", "https://api.example.com/data", errors.New("timeout"), 30*time.Second)

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

func TestRequestSentEventFields(t *testing.T) {
	e := &RequestSent{
		Context:      context.Background(),
		Method:       "PUT",
		URL:          "https://api.example.com/resource/123",
		StatusCode:   204,
		DurationMs:   75,
		RequestSize:  2048,
		ResponseSize: 0,
		TraceID:      "trace-xyz",
		SpanID:       "span-abc",
		ParentID:     "parent-def",
	}

	if e.Name() != "http.request.sent" {
		t.Errorf("Name() = %q, want %q", e.Name(), "http.request.sent")
	}
	if e.Method != "PUT" {
		t.Errorf("Method = %q, want %q", e.Method, "PUT")
	}
	if e.StatusCode != 204 {
		t.Errorf("StatusCode = %d, want 204", e.StatusCode)
	}
	if e.RequestSize != 2048 {
		t.Errorf("RequestSize = %d, want 2048", e.RequestSize)
	}
}

func TestRequestFailedEventFields(t *testing.T) {
	e := &RequestFailed{
		Context:    context.Background(),
		Method:     "DELETE",
		URL:        "https://api.example.com/resource/456",
		Error:      "server unavailable",
		DurationMs: 10000,
		TraceID:    "trace-err",
		SpanID:     "span-err",
		ParentID:   "",
	}

	if e.Name() != "http.request.failed" {
		t.Errorf("Name() = %q, want %q", e.Name(), "http.request.failed")
	}
	if e.Error != "server unavailable" {
		t.Errorf("Error = %q, want %q", e.Error, "server unavailable")
	}
	if e.DurationMs != 10000 {
		t.Errorf("DurationMs = %d, want 10000", e.DurationMs)
	}
}
