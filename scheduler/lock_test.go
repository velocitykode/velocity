package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestInMemoryLocker_FencingToken covers Task 7e: tokens are monotonically
// increasing across successful Acquire calls.
func TestInMemoryLocker_FencingToken(t *testing.T) {
	l := NewInMemoryLocker()
	ctx := context.Background()

	a, err := l.Acquire(ctx, "job.x", time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	firstToken := a.FencingToken()

	if _, err := l.Acquire(ctx, "job.x", time.Second); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("expected ErrLockHeld on contested acquire, got %v", err)
	}

	if err := a.Release(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}
	// Release is idempotent.
	if err := a.Release(ctx); err != nil {
		t.Fatalf("second release should be a no-op, got %v", err)
	}

	b, err := l.Acquire(ctx, "job.x", time.Second)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if b.FencingToken() <= firstToken {
		t.Fatalf("fencing token did not advance: %d -> %d", firstToken, b.FencingToken())
	}
}

// TestInMemoryLocker_TTLExpiry validates that a lock whose TTL has lapsed is
// considered free and can be re-acquired by a different caller.
func TestInMemoryLocker_TTLExpiry(t *testing.T) {
	l := NewInMemoryLocker()
	// Use a fake clock so the test is deterministic.
	now := time.Unix(0, 0)
	l.nowFn = func() time.Time { return now }

	ctx := context.Background()
	if _, err := l.Acquire(ctx, "job", 100*time.Millisecond); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	// Still within TTL — second acquire should fail.
	if _, err := l.Acquire(ctx, "job", time.Second); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("expected ErrLockHeld, got %v", err)
	}

	// Advance fake clock past the TTL and re-acquire.
	now = now.Add(200 * time.Millisecond)
	if _, err := l.Acquire(ctx, "job", time.Second); err != nil {
		t.Fatalf("expected successful re-acquire after TTL, got %v", err)
	}
}
