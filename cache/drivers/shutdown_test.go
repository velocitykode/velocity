package drivers

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryStore_Shutdown_RespectsContextDeadline(t *testing.T) {
	store := NewMemoryStore("")
	store.Start()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := store.Shutdown(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestMemoryStore_Shutdown_Idempotent(t *testing.T) {
	store := NewMemoryStore("")
	store.Start()

	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown failed: %v", err)
	}
	// Second call must not panic on the double-close.
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown failed: %v", err)
	}
}

func TestMemoryStore_Shutdown_StopsCleanupGoroutine(t *testing.T) {
	store := NewMemoryStore("")
	store.Start()

	// No deterministic way to observe the goroutine apart from waiting on
	// Shutdown to complete without hanging. goleak in the integration test
	// provides the real assertion.
	done := make(chan error, 1)
	go func() { done <- store.Shutdown(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown returned error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Shutdown blocked past 500ms")
	}
}

func TestFileStore_Shutdown_RespectsContextDeadline(t *testing.T) {
	store, err := NewFileStore("", t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	store.Start()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestFileStore_Shutdown_Idempotent(t *testing.T) {
	store, err := NewFileStore("", t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	store.Start()
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}
