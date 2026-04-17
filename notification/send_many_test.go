package notification

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestSendMany_AggregatesErrors covers Task 4: failures across parallel sends
// are joined via errors.Join so callers can see every failure, not just the
// first observed.
func TestSendMany_AggregatesErrors(t *testing.T) {
	mgr := NewManager()

	// A channel that always fails with a distinct error.
	boom := errors.New("velocity/test: boom")
	mgr.SetChannel("test", &testChannel{err: boom})

	notifiables := []interface{}{
		&testNotifiable{email: "a@example.com", id: "1"},
		&testNotifiable{email: "b@example.com", id: "2"},
		&testNotifiable{email: "c@example.com", id: "3"},
	}

	err := mgr.SendMany(context.Background(), notifiables, &testNotification{subject: "X", channels: []string{"test"}})
	if err == nil {
		t.Fatal("expected an error from SendMany")
	}

	// errors.Is still finds the underlying cause via errors.Join.
	if !errors.Is(err, boom) {
		t.Errorf("errors.Is(err, boom) = false; want true. err=%v", err)
	}

	// Message should include the "N of M" prefix.
	if !strings.Contains(err.Error(), "3 of 3 sends failed") {
		t.Errorf("want '3 of 3 sends failed' in message, got %q", err.Error())
	}
}

// TestSendMany_Empty returns nil without spawning goroutines when the
// notifiables list is empty.
func TestSendMany_Empty(t *testing.T) {
	mgr := NewManager()
	if err := mgr.SendMany(context.Background(), nil, &testNotification{channels: []string{"test"}}); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}
