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

// TestInMemoryLocker_TokensOverlapAcrossInstances pins the documented
// limitation: each InMemoryLocker carries its own
// per-process counter, so two instances (standing in for two hosts) mint
// overlapping fencing tokens for the same lock name. This is why InMemoryLocker
// must not back OnOneServer / WithoutOverlapping across multiple hosts -
// cross-host fencing requires a distributed Locker with cluster-wide tokens.
func TestInMemoryLocker_TokensOverlapAcrossInstances(t *testing.T) {
	ctx := context.Background()
	hostA := NewInMemoryLocker()
	hostB := NewInMemoryLocker()

	a, err := hostA.Acquire(ctx, "job.shared", time.Second)
	if err != nil {
		t.Fatalf("hostA acquire: %v", err)
	}
	b, err := hostB.Acquire(ctx, "job.shared", time.Second)
	if err != nil {
		t.Fatalf("hostB acquire: %v", err)
	}

	// Both fresh instances start their counter at the same value, so the tokens
	// collide instead of being globally monotonic. If this ever stops being
	// true the doc comments on InMemoryLocker / FencingToken must be revisited.
	if a.FencingToken() != b.FencingToken() {
		t.Fatalf("expected overlapping tokens across instances, got %d and %d",
			a.FencingToken(), b.FencingToken())
	}
}

// TestInMemoryLocker_TokensIndependentPerName confirms tokens advance per lock
// name independently of each other within a single process. The global counter
// is shared, so different names observe distinct (still strictly increasing)
// values rather than a per-name sequence resetting to 1.
func TestInMemoryLocker_TokensIndependentPerName(t *testing.T) {
	ctx := context.Background()
	l := NewInMemoryLocker()

	x, err := l.Acquire(ctx, "job.x", time.Second)
	if err != nil {
		t.Fatalf("acquire job.x: %v", err)
	}
	y, err := l.Acquire(ctx, "job.y", time.Second)
	if err != nil {
		t.Fatalf("acquire job.y: %v", err)
	}

	if y.FencingToken() <= x.FencingToken() {
		t.Fatalf("expected strictly increasing tokens across names, got %d then %d",
			x.FencingToken(), y.FencingToken())
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
