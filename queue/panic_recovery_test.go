package queue

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestBatch_ThenCallback_RecoversPanic verifies that a panic inside a
// user-supplied Then callback does not tear down the worker and that
// Finally still fires.
func TestBatch_ThenCallback_RecoversPanic(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	var finallyCalled atomic.Bool

	jobs := []Job{&testBatchJob{}, &testBatchJob{}}

	batch, err := NewBatch(jobs...).
		Then(func(b *Batch) { panic("then boom") }).
		Finally(func(b *Batch) { finallyCalled.Store(true) }).
		Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	batch.recordSuccess()
	batch.recordSuccess()

	// Allow the async callbacks to fire.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if finallyCalled.Load() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !finallyCalled.Load() {
		t.Fatal("expected Finally callback to run even if Then panicked")
	}
}

// TestBatch_FinallyCallback_RecoversPanic verifies that a panic inside
// Finally is recovered (the worker must keep running — no process crash).
func TestBatch_FinallyCallback_RecoversPanic(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	jobs := []Job{&testBatchJob{}}

	batch, err := NewBatch(jobs...).
		Finally(func(b *Batch) { panic("finally boom") }).
		Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	batch.recordSuccess()

	// Give the recover goroutine a moment — if it doesn't recover the
	// process would exit. The test process surviving to the end of this
	// function is the assertion.
	time.Sleep(100 * time.Millisecond)
}

// TestBatch_CatchCallback_RecoversPanic verifies that a panic inside
// Catch does not tear down the worker.
func TestBatch_CatchCallback_RecoversPanic(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	var finallyCalled atomic.Bool

	jobs := []Job{&testBatchJob{}}

	batch, err := NewBatch(jobs...).
		AllowFailures().
		Catch(func(b *Batch, err error) { panic("catch boom") }).
		Finally(func(b *Batch) { finallyCalled.Store(true) }).
		Dispatch(driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	batch.recordFailure(errFailure)

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if finallyCalled.Load() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !finallyCalled.Load() {
		t.Fatal("expected Finally callback to run even if Catch panicked")
	}
}

// errFailure is a sentinel error used by the panic-recovery tests.
var errFailure = sentinelErr("synthetic failure")

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }
