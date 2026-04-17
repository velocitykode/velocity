package httpclient

import (
	"context"
	"errors"
	"testing"
)

func TestClient_Shutdown_RespectsContextDeadline(t *testing.T) {
	c := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestClient_Shutdown_NoOpOnReady(t *testing.T) {
	c := New()
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
}
