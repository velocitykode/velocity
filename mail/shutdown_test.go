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

func TestManager_Shutdown_NoOp_CanceledContext(t *testing.T) {
	// An empty manager is a nil no-op even with a canceled context: there are
	// no children to propagate cancellation, so there is nothing to fail.
	m := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
}

func TestManager_Shutdown_PropagatesContextToChildren(t *testing.T) {
	// Cancellation reaches children through the provided ctx; a child that
	// honors it surfaces its error via the joined result.
	m := NewManager()
	sm := &ctxAwareMockMailer{}
	m.SetChannel("ch", sm)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// ctxAwareMockMailer's Shutdown returns the context's error, proving the
// manager hands the caller's ctx to each child.
type ctxAwareMockMailer struct {
	mockMailer
}

func (m *ctxAwareMockMailer) Shutdown(ctx context.Context) error {
	return ctx.Err()
}
