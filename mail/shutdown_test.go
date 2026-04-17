package mail

import (
	"context"
	"errors"
	"testing"
)

func TestManager_Shutdown_NoOp(t *testing.T) {
	m := NewManager()
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
}

func TestManager_Shutdown_RespectsContextDeadline(t *testing.T) {
	m := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
