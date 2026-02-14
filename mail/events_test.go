package mail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/velocitykode/velocity/trace"
)

func TestEventNames(t *testing.T) {
	tests := []struct {
		name     string
		event    interface{ Name() string }
		expected string
	}{
		{"MailSent", &MailSent{}, "mail.sent"},
		{"MailFailed", &MailFailed{}, "mail.failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.Name(); got != tt.expected {
				t.Errorf("Name() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestMailDispatcher(t *testing.T) {
	t.Run("SetEventDispatcher", func(t *testing.T) {
		manager := NewManager()
		manager.SetEventDispatcher(nil)

		called := false
		manager.SetEventDispatcher(func(event interface{}) error {
			called = true
			return nil
		})

		manager.dispatchEvent(&MailSent{})

		if !called {
			t.Error("dispatcher was not called")
		}

		manager.SetEventDispatcher(nil)
	})

	t.Run("dispatchEvent with nil dispatcher", func(t *testing.T) {
		manager := NewManager()
		manager.SetEventDispatcher(nil)
		// Should not panic
		manager.dispatchEvent(&MailSent{})
	})

	t.Run("dispatchEvent with error returning dispatcher", func(t *testing.T) {
		manager := NewManager()
		manager.SetEventDispatcher(func(event interface{}) error {
			return errors.New("dispatcher error")
		})

		// Should not panic
		manager.dispatchEvent(&MailSent{})

		manager.SetEventDispatcher(nil)
	})
}

func TestDispatchMailSent(t *testing.T) {
	var captured *MailSent
	dispatch := func(event interface{}) {
		if e, ok := event.(*MailSent); ok {
			captured = e
		}
	}

	t.Run("basic dispatch", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		to := []string{"user@example.com", "other@example.com"}
		dispatchMailSent(dispatch, ctx, to, "Welcome!", "smtp", 150*time.Millisecond)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if len(captured.To) != 2 {
			t.Errorf("To has %d recipients, want 2", len(captured.To))
		}
		if captured.To[0] != "user@example.com" {
			t.Errorf("To[0] = %q, want %q", captured.To[0], "user@example.com")
		}
		if captured.Subject != "Welcome!" {
			t.Errorf("Subject = %q, want %q", captured.Subject, "Welcome!")
		}
		if captured.Channel != "smtp" {
			t.Errorf("Channel = %q, want %q", captured.Channel, "smtp")
		}
		if captured.DurationMs != 150 {
			t.Errorf("DurationMs = %d, want 150", captured.DurationMs)
		}
	})

	t.Run("with trace context", func(t *testing.T) {
		captured = nil
		ctx := trace.WithTrace(context.Background(), "trace-mail", "parent-mail")
		ctx = trace.WithSpan(ctx, "span-mail")
		dispatchMailSent(dispatch, ctx, []string{"test@example.com"}, "Test", "log", 50*time.Millisecond)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.TraceID != "trace-mail" {
			t.Errorf("TraceID = %q, want %q", captured.TraceID, "trace-mail")
		}
		if captured.SpanID != "span-mail" {
			t.Errorf("SpanID = %q, want %q", captured.SpanID, "span-mail")
		}
		if captured.ParentID != "parent-mail" {
			t.Errorf("ParentID = %q, want %q", captured.ParentID, "parent-mail")
		}
	})

	t.Run("with nil dispatch", func(t *testing.T) {
		// Should not panic
		dispatchMailSent(nil, context.Background(), []string{"test@example.com"}, "Test", "log", 50*time.Millisecond)
	})
}

func TestDispatchMailFailed(t *testing.T) {
	var captured *MailFailed
	dispatch := func(event interface{}) {
		if e, ok := event.(*MailFailed); ok {
			captured = e
		}
	}

	t.Run("basic dispatch", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		err := errors.New("SMTP connection failed")
		dispatchMailFailed(dispatch, ctx, []string{"user@example.com"}, "Important", "smtp", err, 5*time.Second)

		if captured == nil {
			t.Fatal("event was not dispatched")
		}
		if captured.Subject != "Important" {
			t.Errorf("Subject = %q, want %q", captured.Subject, "Important")
		}
		if captured.Error != "SMTP connection failed" {
			t.Errorf("Error = %q, want %q", captured.Error, "SMTP connection failed")
		}
		if captured.DurationMs != 5000 {
			t.Errorf("DurationMs = %d, want 5000", captured.DurationMs)
		}
	})

	t.Run("with nil error", func(t *testing.T) {
		captured = nil
		ctx := context.Background()
		dispatchMailFailed(dispatch, ctx, []string{"user@example.com"}, "Test", "log", nil, 100*time.Millisecond)

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
		dispatchMailFailed(dispatch, ctx, []string{"test@example.com"}, "Failed", "postmark", errors.New("timeout"), 30*time.Second)

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

	t.Run("with nil dispatch", func(t *testing.T) {
		// Should not panic
		dispatchMailFailed(nil, context.Background(), []string{"test@example.com"}, "Test", "log", nil, 100*time.Millisecond)
	})
}

func TestMailSentEventFields(t *testing.T) {
	e := &MailSent{
		Context:    context.Background(),
		To:         []string{"a@example.com", "b@example.com"},
		Subject:    "Newsletter",
		Channel:    "mailgun",
		DurationMs: 250,
		TraceID:    "trace-xyz",
		SpanID:     "span-abc",
		ParentID:   "parent-def",
	}

	if e.Name() != "mail.sent" {
		t.Errorf("Name() = %q, want %q", e.Name(), "mail.sent")
	}
	if len(e.To) != 2 {
		t.Errorf("To has %d recipients, want 2", len(e.To))
	}
	if e.Channel != "mailgun" {
		t.Errorf("Channel = %q, want %q", e.Channel, "mailgun")
	}
}

func TestMailFailedEventFields(t *testing.T) {
	e := &MailFailed{
		Context:    context.Background(),
		To:         []string{"user@example.com"},
		Subject:    "Password Reset",
		Channel:    "smtp",
		Error:      "authentication failed",
		DurationMs: 1000,
		TraceID:    "trace-err",
		SpanID:     "span-err",
		ParentID:   "",
	}

	if e.Name() != "mail.failed" {
		t.Errorf("Name() = %q, want %q", e.Name(), "mail.failed")
	}
	if e.Error != "authentication failed" {
		t.Errorf("Error = %q, want %q", e.Error, "authentication failed")
	}
}
