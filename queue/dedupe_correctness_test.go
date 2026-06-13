package queue

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestMemoryDriver_Failed_CallbackReentryNoDeadlock asserts that a job
// whose Failed callback re-enters the driver completes instead of
// self-deadlocking. Before the fix, Failed held m.mu (write lock) across
// job.Failed via a deferred unlock, so any re-entry that took the lock
// (Size takes the read lock) blocked forever. This mirrors the hazard
// FailReservedCtx already guards against.
func TestMemoryDriver_Failed_CallbackReentryNoDeadlock(t *testing.T) {
	d := NewMemoryDriver()

	done := make(chan struct{})
	job := &TestJob{
		ID: "reentrant",
		OnFail: func(error) {
			// Re-enter the driver from within the Failed callback.
			_, _ = d.Size("requeue")
			close(done)
		},
	}

	go func() {
		_ = d.Failed(job, errors.New("boom"), "requeue")
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Failed callback re-entering the driver deadlocked")
	}
}

// TestMemoryDriver_SweepStaleDedupeKeys asserts the background sweep
// prunes dedupe keys older than dedupeKeyRetention while leaving fresh
// keys intact, so the set does not leak for the process lifetime. Keys
// are written directly under m.mu (same-package access) with a back-dated
// insertion time to stand in for the passage of dedupeKeyRetention.
func TestMemoryDriver_SweepStaleDedupeKeys(t *testing.T) {
	d := NewMemoryDriver()

	now := time.Now()
	d.mu.Lock()
	d.dedupeKeys["stale"] = now.Add(-dedupeKeyRetention - time.Hour)
	d.dedupeKeys["fresh"] = now
	d.mu.Unlock()

	d.sweepStaleDedupeKeys(now)

	d.mu.Lock()
	_, stalePresent := d.dedupeKeys["stale"]
	_, freshPresent := d.dedupeKeys["fresh"]
	d.mu.Unlock()

	if stalePresent {
		t.Fatal("stale dedupe key survived the sweep")
	}
	if !freshPresent {
		t.Fatal("fresh dedupe key was incorrectly swept")
	}
}

// TestMemoryDriver_SweepStaleDedupeKeys_KeepsLiveKey asserts the sweep
// does NOT prune a dedupe key whose job is still live (queued, delayed, or
// reserved), even when its insertion time is well past dedupeKeyRetention.
// A job that outlives the horizon while in flight (e.g. repeatedly released
// with backoff) must keep its key so a second PushIfNotExistsCtx with the
// same key cannot enqueue a duplicate, preserving at-most-once.
func TestMemoryDriver_SweepStaleDedupeKeys_KeepsLiveKey(t *testing.T) {
	d := NewMemoryDriver()
	ctx := context.Background()
	const key = "batch-1:callback"

	if err := d.PushIfNotExistsCtx(ctx, &TestJob{ID: "a"}, key, "q"); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Back-date the insertion so the age check alone would prune it.
	now := time.Now()
	d.mu.Lock()
	d.dedupeKeys[key] = now.Add(-dedupeKeyRetention - time.Hour)
	d.mu.Unlock()

	d.sweepStaleDedupeKeys(now)

	d.mu.Lock()
	_, present := d.dedupeKeys[key]
	d.mu.Unlock()
	if !present {
		t.Fatal("live dedupe key was pruned by the sweep, allowing a duplicate enqueue")
	}

	// A re-push with the same key must still no-op while the job is live.
	if err := d.PushIfNotExistsCtx(ctx, &TestJob{ID: "b"}, key, "q"); err != nil {
		t.Fatalf("re-push: %v", err)
	}
	if n, _ := d.Size("q"); n != 1 {
		t.Fatalf("duplicate enqueued after sweep: want size 1, got %d", n)
	}
}

// TestDatabaseDriver_Clear_ReleasesDedupeKeys asserts Clear deletes the
// queue's job_dedupe rows alongside its jobs, so a post-Clear push with a
// previously seen dedupe key re-enqueues instead of silently no-op'ing
// against a stale dedupe row. This aligns DatabaseDriver.Clear with the
// memory driver's queue-scoped key release.
func TestDatabaseDriver_Clear_ReleasesDedupeKeys(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	// The base harness does not create job_dedupe; add it.
	for _, ddl := range JobDedupeMigrationSQL("sqlite") {
		if _, err := driver.db.Exec(ddl); err != nil {
			t.Fatalf("job_dedupe schema: %v", err)
		}
	}

	ctx := context.Background()
	const key = "batch-1:callback"

	if err := driver.PushIfNotExistsCtx(ctx, &TestJob{ID: "a"}, key, "q"); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if n, _ := driver.Size("q"); n != 1 {
		t.Fatalf("want size 1 after first push, got %d", n)
	}

	if err := driver.Clear("q"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if n, _ := driver.Size("q"); n != 0 {
		t.Fatalf("want size 0 after clear, got %d", n)
	}

	// Same key after Clear must re-enqueue, proving the dedupe row was
	// released. Without the Clear-side delete this push no-ops.
	if err := driver.PushIfNotExistsCtx(ctx, &TestJob{ID: "b"}, key, "q"); err != nil {
		t.Fatalf("re-push: %v", err)
	}
	if n, _ := driver.Size("q"); n != 1 {
		t.Fatalf("re-push after Clear should re-enqueue (want size 1), got %d", n)
	}
}
