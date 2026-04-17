package stores

import (
	"context"
	"errors"
	"testing"
)

func TestSessionStore_Shutdown_RespectsContextDeadline(t *testing.T) {
	store := NewSessionStore()
	store.Start(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestSessionStore_Shutdown_Idempotent(t *testing.T) {
	store := NewSessionStore()
	store.Start(context.Background())

	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}
