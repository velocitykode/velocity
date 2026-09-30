package queue

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/hostile"
)

// A job failure whose error text cannot be read (its Error panics) is
// recorded once, with the fixed text as the batch's last error, and the
// named failure callback is dispatched with that text; nothing panics.
func TestBatch_FailureWithUnreadableErrorText(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()
	batch, err := NewBatch(&testBatchJob{}, &testBatchJob{}).
		AllowFailures().
		OnFailed("unreadable-failure").
		Dispatch(context.Background(), driver)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if p := hostile.Within(t, hostile.Deadline, func() { batch.recordFailure(context.Background(), isPanics{}) }); p != nil {
		t.Fatalf("recordFailure panicked: %v", p)
	}
	if got := batch.FailedJobs(); got != 1 {
		t.Errorf("failed jobs = %d, want 1", got)
	}
	updated, _ := FindBatch(batch.ID())
	if updated == nil || updated.lastErrorSnapshot() != errchain.Unreadable {
		t.Errorf("last error = %q, want the fixed text", updated.lastErrorSnapshot())
	}
}

// The database repository stores the fixed text as last_error for a job
// error whose Error panics, and counts the failure once.
func TestDatabaseBatchRepository_IncrementFailureUnreadableText(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()
	repo, err := NewDatabaseBatchRepository(db, "sqlite")
	if err != nil {
		t.Fatalf("new repo: %v", err)
	}
	b := &Batch{id: newBatchID(), totalJobs: 2, queue: "default"}
	b.pendingJobs.Store(2)
	if err := repo.Save(context.Background(), b); err != nil {
		t.Fatalf("save: %v", err)
	}
	var updated *Batch
	if p := hostile.Within(t, hostile.Deadline, func() {
		updated, _, err = repo.IncrementFailure(context.Background(), b.id, isPanics{})
	}); p != nil {
		t.Fatalf("IncrementFailure panicked: %v", p)
	}
	if err != nil {
		t.Fatalf("IncrementFailure: %v", err)
	}
	if updated.FailedJobs() != 1 || updated.lastErrorSnapshot() != errchain.Unreadable {
		t.Fatalf("failed %d, last error %q; want 1 and the fixed text", updated.FailedJobs(), updated.lastErrorSnapshot())
	}
}
