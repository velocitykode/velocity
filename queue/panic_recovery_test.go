package queue

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	testsync "github.com/velocitykode/velocity/testing"
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
		Dispatch(context.Background(), driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	batch.recordSuccess(context.Background())
	batch.recordSuccess(context.Background())

	testsync.Eventually(t, finallyCalled.Load, time.Second, "Finally runs even after Then panics")
}

// TestBatch_FinallyCallback_RecoversPanic verifies that a panic inside
// Finally is recovered (the worker must keep running — no process crash).
func TestBatch_FinallyCallback_RecoversPanic(t *testing.T) {
	resetBatchStoreForTest(t)
	driver := newMemoryDriver()

	var finallyEntered atomic.Bool
	jobs := []Job{&testBatchJob{}}

	batch, err := NewBatch(jobs...).
		Finally(func(b *Batch) {
			finallyEntered.Store(true)
			panic("finally boom")
		}).
		Dispatch(context.Background(), driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	batch.recordSuccess(context.Background())

	// Wait until Finally has been entered. If recover is broken the process
	// dies before Eventually returns; surviving = recovery works.
	testsync.Eventually(t, finallyEntered.Load, time.Second, "Finally callback entered")
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
		Dispatch(context.Background(), driver)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	batch.recordFailure(context.Background(), errFailure)

	testsync.Eventually(t, finallyCalled.Load, time.Second, "Finally runs even after Catch panics")
}

// errFailure is a sentinel error used by the panic-recovery tests.
var errFailure = sentinelErr("synthetic failure")

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }
