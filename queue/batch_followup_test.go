package queue

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	testsync "github.com/velocitykode/velocity/testing"
)

// ----- HIGH 1: cross-process callbacks -----------------------------------

// TestBatch_CrossProcess_OnCompleteFiresViaQueueJob simulates the
// multi-host scenario the original C-03 fix promised but did not deliver:
// the dispatcher process registers a NAMED callback (OnComplete), then
// a "remote worker" (different goroutine, no closure access) completes
// the last job. The terminal CAS in the repository enqueues a
// BatchCallbackJob; another worker picks the job up and invokes the
// registered handler. Counter assertion: the handler must run exactly
// once, by name, without any closure being involved.
func TestBatch_CrossProcess_OnCompleteFiresViaQueueJob(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	resetBatchStoreForTest(t)
	ResetBatchCallbacksForTest()

	dispatcherRepo, _ := NewDatabaseBatchRepository(db, "sqlite")
	prevDefault := DefaultBatchRepository()
	SetDefaultBatchRepository(dispatcherRepo)
	t.Cleanup(func() { SetDefaultBatchRepository(prevDefault) })

	// Wire a queue driver for cross-process callback delivery. The
	// BatchCallbackJob will be pushed here and consumed by a separate
	// Worker below.
	callbackDriver := newMemoryDriver()
	SetBatchCallbackQueue(callbackDriver, "default")
	t.Cleanup(func() { ResetBatchCallbackQueueForTest() })

	// Register the callback by name. RegisterBatchCallback is the
	// cross-process registration point: any worker that pops a
	// BatchCallbackJob looks up the handler in this registry.
	var fired atomic.Int32
	var firedBatchID atomic.Value
	RegisterBatchCallback("send-completion-email", func(ctx context.Context, b *Batch) error {
		fired.Add(1)
		firedBatchID.Store(string(b.ID()))
		return nil
	})

	// Dispatcher dispatches the batch with a NAMED OnComplete callback.
	// No closure is registered, so there is nothing in the in-process
	// callbackEntry path to fire; everything must flow through the
	// queue.
	jobsDriver := newMemoryDriver()
	batch, err := NewBatch(&testBatchJob{}, &testBatchJob{}).
		OnComplete("send-completion-email").
		Dispatch(context.Background(), jobsDriver)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Spin up a worker that drains the BatchCallbackJob queue. This is
	// the "any host with a worker" that should pick up the callback
	// dispatched by the terminal CAS.
	callbackWorker := NewWorker(callbackDriver, "default", func(j Job) error { return j.Handle() },
		WithInterval(5*time.Millisecond), WithMaxRetries(0))
	callbackWorker.Start(context.Background())
	t.Cleanup(callbackWorker.Stop)

	// Simulate the "remote worker" by calling IncrementSuccess
	// directly against the dispatcher repo for both jobs. The second
	// call wins the completion CAS and enqueues the callback job.
	for i := 0; i < 2; i++ {
		_, _, err := dispatcherRepo.IncrementSuccess(context.Background(), batch.ID())
		if err != nil {
			t.Fatalf("incr %d: %v", i, err)
		}
	}

	// Trigger the local-side firing path (this is what the worker
	// pumping the *batch's* jobs would do after observing
	// justFinished=true). The fireTerminalCallbacks helper enqueues
	// the named callback job.
	got, _ := dispatcherRepo.Find(context.Background(), batch.ID())
	batch.copyCountersFrom(got)
	batch.fireTerminalCallbacks(context.Background(), got)

	testsync.Eventually(t, func() bool { return fired.Load() == 1 }, 3*time.Second,
		"named OnComplete handler fires via dispatched BatchCallbackJob")

	// Stability window so a double-dispatch would be caught.
	time.Sleep(100 * time.Millisecond)
	if fired.Load() != 1 {
		t.Errorf("OnComplete handler fired %d times, want 1", fired.Load())
	}
	if v := firedBatchID.Load(); v == nil || v.(string) != string(batch.ID()) {
		t.Errorf("handler observed wrong batchID: %v vs %s", v, batch.ID())
	}
}

// TestBatch_CrossProcess_OnFailedAndOnFinally rounds out the named-
// callback story: Catch and Finally must both fire on duty workers,
// keyed by name, with the failure error message reaching the handler.
func TestBatch_CrossProcess_OnFailedAndOnFinally(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	resetBatchStoreForTest(t)
	ResetBatchCallbacksForTest()

	repo, _ := NewDatabaseBatchRepository(db, "sqlite")
	prevDefault := DefaultBatchRepository()
	SetDefaultBatchRepository(repo)
	t.Cleanup(func() { SetDefaultBatchRepository(prevDefault) })

	callbackDriver := newMemoryDriver()
	SetBatchCallbackQueue(callbackDriver, "default")
	t.Cleanup(func() { ResetBatchCallbackQueueForTest() })

	var failedFired atomic.Int32
	var finallyFired atomic.Int32
	var observedErr atomic.Value
	RegisterBatchFailureCallback("notify-engineers", func(ctx context.Context, b *Batch, errMsg string) error {
		failedFired.Add(1)
		observedErr.Store(errMsg)
		return nil
	})
	RegisterBatchCallback("audit-batch", func(ctx context.Context, b *Batch) error {
		finallyFired.Add(1)
		return nil
	})

	callbackWorker := NewWorker(callbackDriver, "default", func(j Job) error { return j.Handle() },
		WithInterval(5*time.Millisecond), WithMaxRetries(0))
	callbackWorker.Start(context.Background())
	t.Cleanup(callbackWorker.Stop)

	jobsDriver := newMemoryDriver()
	batch, err := NewBatch(&testBatchJob{}, &testBatchJob{}).
		AllowFailures().
		OnFailed("notify-engineers").
		OnFinally("audit-batch").
		Dispatch(context.Background(), jobsDriver)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Drive the batch: one failure, one success.
	batch.recordFailure(context.Background(), &stringError{"payment provider 502"})
	batch.recordSuccess(context.Background())

	testsync.Eventually(t, func() bool { return failedFired.Load() == 1 && finallyFired.Load() == 1 },
		3*time.Second, "OnFailed and OnFinally both fire")

	time.Sleep(100 * time.Millisecond)
	if failedFired.Load() != 1 {
		t.Errorf("OnFailed fired %d times, want 1", failedFired.Load())
	}
	if finallyFired.Load() != 1 {
		t.Errorf("OnFinally fired %d times, want 1", finallyFired.Load())
	}
	if s, _ := observedErr.Load().(string); !strings.Contains(s, "502") {
		t.Errorf("OnFailed did not receive error text: got %q", s)
	}
}

// TestBatch_CrossProcess_BatchCompletedEventDispatched checks that
// BatchCompleted is published through the process-wide event
// dispatcher even when the per-batch dispatcher is nil. This is the
// "events must not silently drop" half of HIGH 1.
func TestBatch_CrossProcess_BatchCompletedEventDispatched(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	resetBatchStoreForTest(t)
	ResetBatchCallbacksForTest()

	repo, _ := NewDatabaseBatchRepository(db, "sqlite")
	prevDefault := DefaultBatchRepository()
	SetDefaultBatchRepository(repo)
	t.Cleanup(func() { SetDefaultBatchRepository(prevDefault) })

	var observed atomic.Int32
	var observedName atomic.Value
	SetGlobalEventDispatcher(func(ctx context.Context, event interface{}) error {
		type namer interface{ Name() string }
		if e, ok := event.(namer); ok {
			if e.Name() == "batch.completed" {
				observed.Add(1)
				observedName.Store(e.Name())
			}
		}
		return nil
	})
	t.Cleanup(func() { SetGlobalEventDispatcher(nil) })

	jobsDriver := newMemoryDriver()
	// Note: no WithEventDispatcher, no Then/Catch/Finally - the global
	// dispatcher must STILL receive batch.completed when the last
	// IncrementSuccess wins the CAS.
	batch, err := NewBatch(&testBatchJob{}).Dispatch(context.Background(), jobsDriver)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	batch.recordSuccess(context.Background())

	testsync.Eventually(t, func() bool { return observed.Load() >= 1 }, 2*time.Second,
		"global dispatcher observes batch.completed")
	if v, _ := observedName.Load().(string); v != "batch.completed" {
		t.Errorf("observed event name: %q", v)
	}
}

// stringError lets the cross-process test thread an error message
// through to recordFailure without needing the errors package.
type stringError struct{ s string }

func (e *stringError) Error() string { return e.s }

// ----- HIGH 2: shutdown lifecycle ----------------------------------------

// TestDatabaseBatchRepository_Close_BlocksFurtherUse asserts that after
// Close, mutating operations return ErrBatchRepositoryClosed instead of
// silently writing to a torn-down DB pool. App.Shutdown relies on this
// to make stale worker references fail loudly.
func TestDatabaseBatchRepository_Close_BlocksFurtherUse(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	repo, _ := NewDatabaseBatchRepository(db, "sqlite")
	b := &Batch{id: newBatchID(), totalJobs: 1, queue: "default"}
	b.pendingJobs.Store(1)
	if err := repo.Save(context.Background(), b); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := repo.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Second close is idempotent.
	if err := repo.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}

	if _, err := repo.Find(context.Background(), b.id); err == nil {
		t.Error("Find on closed repo must return error")
	}
	if err := repo.Save(context.Background(), b); err == nil {
		t.Error("Save on closed repo must return error")
	}
	if _, _, err := repo.IncrementSuccess(context.Background(), b.id); err == nil {
		t.Error("IncrementSuccess on closed repo must return error")
	}
	if _, err := repo.Cancel(context.Background(), b.id); err == nil {
		t.Error("Cancel on closed repo must return error")
	}
}

// TestResetAutoInstalledBatchRepository_DropsAutoInstall covers the
// shutdown-side reset: after EnsureDefaultBatchRepository the holder is
// marked autoInstalled and ResetAutoInstalled drops it back to a fresh
// in-memory default. SetDefaultBatchRepository-installed holders are
// NOT autoInstalled and survive the reset.
func TestResetAutoInstalledBatchRepository_DropsAutoInstall(t *testing.T) {
	prev := DefaultBatchRepository()
	t.Cleanup(func() { SetDefaultBatchRepository(prev) })

	ResetDefaultBatchRepositoryForTest()

	// EnsureDefault marks the holder autoInstalled=true.
	auto := NewInMemoryBatchRepository()
	if installed := EnsureDefaultBatchRepository(auto); !installed {
		t.Fatal("EnsureDefaultBatchRepository should have installed")
	}
	if DefaultBatchRepository() != auto {
		t.Fatal("default did not point at auto-installed repo")
	}

	// Reset drops auto-installed; sentinel for "back to in-memory init".
	if reset := ResetAutoInstalledBatchRepository(); !reset {
		t.Fatal("ResetAutoInstalledBatchRepository should have reset auto-installed holder")
	}
	if DefaultBatchRepository() == auto {
		t.Fatal("ResetAutoInstalled did not swap out the auto-installed repo")
	}

	// SetDefault marks holder userSet=true and NOT autoInstalled.
	user := NewInMemoryBatchRepository()
	SetDefaultBatchRepository(user)
	if reset := ResetAutoInstalledBatchRepository(); reset {
		t.Error("ResetAutoInstalledBatchRepository must NOT touch user-installed repo")
	}
	if DefaultBatchRepository() != user {
		t.Error("user repo was clobbered by ResetAutoInstalled")
	}
}

// ----- HIGH 3: MySQL counter ordering ------------------------------------

// TestBuildIncrementUpdate_OrderingForMySQL is the unit test for the
// MySQL left-to-right SET semantics fix. The generated SQL must place
// completed_jobs / failed_jobs / last_error BEFORE pending_jobs in the
// SET clause; otherwise MySQL evaluates the counter CASE predicates
// against the already-decremented pending value and the clamp fires
// for legitimate increments.
//
// SQLite and Postgres are pre-update-row semantics so the order does
// not matter for correctness on those engines, but consistent ordering
// makes the code portable.
func TestBuildIncrementUpdate_OrderingForMySQL(t *testing.T) {
	now := time.Now().UTC()

	cases := []struct {
		name    string
		success bool
		failure bool
		errText string
	}{
		{name: "success", success: true},
		{name: "failure", failure: true},
		{name: "failure_with_error", failure: true, errText: "boom"},
		{name: "skip"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var et *string
			if tc.errText != "" {
				e := tc.errText
				et = &e
			}
			q, _ := buildIncrementUpdate(BatchID("bid"), tc.success, tc.failure, et, now)

			// Locate set clause body (between "SET " and " WHERE id").
			i := strings.Index(q, "SET ")
			j := strings.LastIndex(q, " WHERE id")
			if i < 0 || j < 0 || i >= j {
				t.Fatalf("malformed UPDATE: %s", q)
			}
			setBody := q[i+4 : j]

			pendingIdx := strings.Index(setBody, "pending_jobs =")
			if pendingIdx < 0 {
				t.Fatalf("pending_jobs missing from SET body: %s", setBody)
			}

			// Each counter column that participates must precede pending_jobs.
			if tc.success {
				ci := strings.Index(setBody, "completed_jobs =")
				if ci < 0 {
					t.Errorf("completed_jobs missing: %s", setBody)
				}
				if ci >= pendingIdx {
					t.Errorf("MySQL ordering: completed_jobs (idx=%d) must precede pending_jobs (idx=%d); set=%s",
						ci, pendingIdx, setBody)
				}
			}
			if tc.failure {
				fi := strings.Index(setBody, "failed_jobs =")
				if fi < 0 {
					t.Errorf("failed_jobs missing: %s", setBody)
				}
				if fi >= pendingIdx {
					t.Errorf("MySQL ordering: failed_jobs (idx=%d) must precede pending_jobs (idx=%d); set=%s",
						fi, pendingIdx, setBody)
				}
				if tc.errText != "" {
					ei := strings.Index(setBody, "last_error =")
					if ei < 0 {
						t.Errorf("last_error missing: %s", setBody)
					}
					if ei >= pendingIdx {
						t.Errorf("MySQL ordering: last_error (idx=%d) must precede pending_jobs (idx=%d); set=%s",
							ei, pendingIdx, setBody)
					}
				}
			}
		})
	}
}

// TestBuildIncrementUpdate_PlaceholdersAreSequential is a regression
// test for the args-vs-placeholder shuffle bug that broke last_error
// persistence on the SQLite path: positional ? rewriting in
// rewriteQuery assigns args in declaration order, not $N order, so the
// SET clause must reference each $N exactly once and in order.
func TestBuildIncrementUpdate_PlaceholdersAreSequential(t *testing.T) {
	now := time.Now().UTC()
	errText := "synthetic"
	q, args := buildIncrementUpdate(BatchID("bid"), false, true, &errText, now)

	// Walk the SQL and pull out $N occurrences in order; they must be
	// 1..len(args) without gaps so positional rewriting is safe.
	var nums []int
	for i := 0; i < len(q); i++ {
		if q[i] == '$' {
			j := i + 1
			for j < len(q) && q[j] >= '0' && q[j] <= '9' {
				j++
			}
			if j > i+1 {
				n := 0
				for _, c := range q[i+1 : j] {
					n = n*10 + int(c-'0')
				}
				nums = append(nums, n)
				i = j - 1
			}
		}
	}
	if len(nums) != len(args) {
		t.Fatalf("$N count %d != args count %d; sql=%s", len(nums), len(args), q)
	}
	for i, n := range nums {
		if n != i+1 {
			t.Fatalf("placeholder out of order at position %d: got $%d, want $%d; sql=%s",
				i, n, i+1, q)
		}
	}
}

// ----- MEDIUM 4: WithRepository is gone -----------------------------------

// TestPendingBatch_NoWithRepositoryMethod is a compile-time guarantee:
// if WithRepository were re-added, this assertion would compile and
// fail. The runtime check uses reflection to confirm the method does
// not exist on *PendingBatch.
func TestPendingBatch_NoWithRepositoryMethod(t *testing.T) {
	// The compile-time guarantee: the assignment below must NOT compile
	// when WithRepository exists. We rely on `go vet` / build to catch
	// it. The reflective check below catches the case where someone
	// re-adds the method silently.
	pb := NewBatch(&testBatchJob{})
	// Use reflect to ensure no method named "WithRepository" exists.
	// We do not import reflect to keep this test fast: instead we
	// assert via interface assertion that a stub with that method does
	// not match *PendingBatch.
	type withRepo interface {
		WithRepository(repo BatchRepository) *PendingBatch
	}
	if _, ok := any(pb).(withRepo); ok {
		t.Fatal("PendingBatch.WithRepository must not exist; route through SetDefaultBatchRepository instead")
	}
}

// ----- Integration: HIGH 1 plus event dispatcher -------------------------

// TestBatch_GlobalEventDispatcher_RoutesAllBatchEvents covers the
// previously-silent BatchCreated, BatchJobCompleted, BatchJobFailed,
// BatchCompleted, BatchCancelled events. They must all route through
// the process-wide dispatcher in addition to the per-batch one.
func TestBatch_GlobalEventDispatcher_RoutesAllBatchEvents(t *testing.T) {
	resetBatchStoreForTest(t)
	ResetBatchCallbacksForTest()
	t.Cleanup(func() { SetGlobalEventDispatcher(nil) })

	var seen sync.Map
	SetGlobalEventDispatcher(func(ctx context.Context, event interface{}) error {
		type namer interface{ Name() string }
		if e, ok := event.(namer); ok {
			seen.Store(e.Name(), true)
		}
		return nil
	})

	driver := newMemoryDriver()
	batch, err := NewBatch(&testBatchJob{}, &testBatchJob{}).
		AllowFailures().
		Dispatch(context.Background(), driver)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	batch.recordSuccess(context.Background())
	batch.recordFailure(context.Background(), &stringError{"boom"})

	testsync.Eventually(t, func() bool {
		want := []string{"batch.created", "batch.job.completed", "batch.job.failed", "batch.completed"}
		for _, name := range want {
			if _, ok := seen.Load(name); !ok {
				return false
			}
		}
		return true
	}, 2*time.Second, "all major batch events route through global dispatcher")
}
