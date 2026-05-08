package orm

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// TestOnCommit_FiresAfterCommit covers the core happy path: a
// callback registered through orm.OnCommit fires once the
// surrounding Manager.Transaction commits. Verifies ordering
// (post-commit, not before), single-shot delivery, and that the
// callback observes a tx that has actually persisted (the commit
// has happened by the time the callback runs).
func TestOnCommit_FiresAfterCommit(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	ctx := PrepareTxCallbacks(context.Background())

	var ran atomic.Bool
	var observedRowCount int
	err := m.Transaction(ctx, func(txCtx context.Context) error {
		if _, err := (User{}).Create(txCtx, map[string]any{
			"name":  "alice",
			"email": "alice@example.com",
			"age":   30,
		}); err != nil {
			return err
		}
		// Register inside the closure so the callbacks holder is
		// definitely live when OnCommit walks ctx.
		return OnCommit(ctx, func(c context.Context) error {
			ran.Store(true)
			// Reading on the parent pool here proves the commit has
			// actually happened: pre-commit reads return zero rows
			// (covered by TestQuery_WithTx_ReadsSeeUncommittedWrites
			// in tx_test.go).
			_ = m.DB().QueryRow(`SELECT COUNT(*) FROM users WHERE email = ?`,
				"alice@example.com").Scan(&observedRowCount)
			return nil
		})
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if !ran.Load() {
		t.Fatal("OnCommit callback did not fire")
	}
	if observedRowCount != 1 {
		t.Errorf("post-commit row count observed by callback = %d, want 1 (callback fired before commit)", observedRowCount)
	}
}

// TestOnCommit_DoesNotFireOnRollback verifies the rollback boundary:
// commit callbacks must not run when the surrounding tx rolls back.
// Without this, code that wires outbox dispatch via OnCommit would
// emit phantom messages for transactions that never committed.
func TestOnCommit_DoesNotFireOnRollback(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	ctx := PrepareTxCallbacks(context.Background())
	var ran atomic.Bool
	sentinel := errors.New("force rollback")
	err := m.Transaction(ctx, func(txCtx context.Context) error {
		if _, err := (User{}).Create(txCtx, map[string]any{
			"name":  "ghost",
			"email": "ghost@example.com",
			"age":   1,
		}); err != nil {
			return err
		}
		if regErr := OnCommit(ctx, func(c context.Context) error {
			ran.Store(true)
			return nil
		}); regErr != nil {
			return regErr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction = %v, want sentinel", err)
	}
	if ran.Load() {
		t.Error("OnCommit callback fired despite rollback")
	}
}

// TestOnRollback_FiresOnRollback covers the symmetric case: an
// OnRollback callback runs when the closure returns an error.
func TestOnRollback_FiresOnRollback(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	ctx := PrepareTxCallbacks(context.Background())
	var ran atomic.Bool
	sentinel := errors.New("rollback")
	err := m.Transaction(ctx, func(txCtx context.Context) error {
		if regErr := OnRollback(ctx, func(c context.Context) error {
			ran.Store(true)
			return nil
		}); regErr != nil {
			return regErr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction = %v, want sentinel", err)
	}
	if !ran.Load() {
		t.Error("OnRollback callback did not fire on rollback")
	}
}

// TestOnRollback_DoesNotFireOnCommit verifies rollback callbacks do
// not run on commit. Symmetric guard for TestOnCommit_DoesNotFireOnRollback.
func TestOnRollback_DoesNotFireOnCommit(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	ctx := PrepareTxCallbacks(context.Background())
	var ran atomic.Bool
	err := m.Transaction(ctx, func(txCtx context.Context) error {
		return OnRollback(ctx, func(c context.Context) error {
			ran.Store(true)
			return nil
		})
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if ran.Load() {
		t.Error("OnRollback callback fired despite successful commit")
	}
}

// TestOnCommit_FiresInRegistrationOrder verifies callbacks run in
// the exact order they were registered. Outbox / fanout pipelines
// often rely on ordering (publish webhook before invalidating cache,
// for example) so this is a contract guarantee, not an
// implementation detail.
func TestOnCommit_FiresInRegistrationOrder(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	ctx := PrepareTxCallbacks(context.Background())
	var order []int
	var mu sync.Mutex
	err := m.Transaction(ctx, func(txCtx context.Context) error {
		for i := 0; i < 5; i++ {
			i := i
			if regErr := OnCommit(ctx, func(c context.Context) error {
				mu.Lock()
				order = append(order, i)
				mu.Unlock()
				return nil
			}); regErr != nil {
				return regErr
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	want := []int{0, 1, 2, 3, 4}
	if len(order) != len(want) {
		t.Fatalf("callback count = %d, want %d", len(order), len(want))
	}
	for i, v := range want {
		if order[i] != v {
			t.Errorf("callback[%d] = %d, want %d (out of order)", i, order[i], v)
		}
	}
}

// TestOnCommit_PanicDoesNotBreakTxState verifies a panicking
// callback is recovered and subsequent callbacks still run. This is
// critical because outbox dispatch fanout often runs many callbacks
// in sequence; one bad listener cannot block the rest.
func TestOnCommit_PanicDoesNotBreakTxState(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	ctx := PrepareTxCallbacks(context.Background())
	var ranAfterPanic atomic.Bool
	err := m.Transaction(ctx, func(txCtx context.Context) error {
		if regErr := OnCommit(ctx, func(c context.Context) error {
			panic("intentional callback panic")
		}); regErr != nil {
			return regErr
		}
		return OnCommit(ctx, func(c context.Context) error {
			ranAfterPanic.Store(true)
			return nil
		})
	})
	if err != nil {
		t.Fatalf("Transaction returned %v despite recovered callback panic", err)
	}
	if !ranAfterPanic.Load() {
		t.Error("subsequent OnCommit callback did not run after sibling panic; recover did not isolate")
	}
}

// TestOnCommit_NoHolderReturnsError covers the misuse path: calling
// OnCommit without wrapping ctx in PrepareTxCallbacks should return
// ErrNoTxCallbacks so callers can detect the misuse explicitly
// rather than silently dropping the callback.
func TestOnCommit_NoHolderReturnsError(t *testing.T) {
	err := OnCommit(context.Background(), func(c context.Context) error {
		return nil
	})
	if !errors.Is(err, ErrNoTxCallbacks) {
		t.Errorf("OnCommit without holder returned %v, want ErrNoTxCallbacks", err)
	}
}

// TestOnCommit_NestedTransactionFiresOnOuterCommit verifies nesting
// semantics: a callback registered inside a nested Transaction call
// fires only on the outermost commit, not when the inner closure
// returns. This matches the buffered events nesting contract (see
// events.InstallBuffer) so the two post-commit primitives behave the
// same way.
func TestOnCommit_NestedTransactionFiresOnOuterCommit(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	ctx := PrepareTxCallbacks(context.Background())
	var ran atomic.Bool
	var firedDuringOuter atomic.Bool

	err := m.Transaction(ctx, func(outerCtx context.Context) error {
		// Inner Transaction reuses the holder slot installed by the
		// outer call, so registrations inside it queue against the
		// outer callbacks list and fire when the outer commits, not
		// when the inner closure returns.
		nested := func(innerCtx context.Context) error {
			return m.Transaction(outerCtx, func(innerCtx context.Context) error {
				return OnCommit(innerCtx, func(c context.Context) error {
					ran.Store(true)
					return nil
				})
			})
		}
		if nestedErr := nested(ctx); nestedErr != nil {
			return nestedErr
		}
		// Inside the outer Transaction, the inner call has returned
		// but the outer callback boundary has not been reached yet.
		// The callback must NOT have fired on inner commit.
		firedDuringOuter.Store(ran.Load())
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if firedDuringOuter.Load() {
		t.Error("nested OnCommit fired on inner-commit boundary; expected outer-commit boundary")
	}
	if !ran.Load() {
		t.Fatal("nested OnCommit never fired after outer commit")
	}
}

// TestOnCommit_RegisteredFromGoroutineSafe verifies the mutex
// protection on TxCallbacks: concurrent registrations from goroutines
// fanned out inside the closure must not race. Combined with -race
// this is a sharp test of the mutex contract (security rule #3).
func TestOnCommit_RegisteredFromGoroutineSafe(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	ctx := PrepareTxCallbacks(context.Background())
	var counter atomic.Int32
	const N = 50

	err := m.Transaction(ctx, func(txCtx context.Context) error {
		var wg sync.WaitGroup
		for i := 0; i < N; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = OnCommit(ctx, func(c context.Context) error {
					counter.Add(1)
					return nil
				})
			}()
		}
		wg.Wait()
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if got := counter.Load(); got != N {
		t.Errorf("commit callback firings = %d, want %d", got, N)
	}
}

// TestOnCommit_RegistrationAfterTerminalIsNoop verifies callbacks
// registered through the *TxCallbacks handle after the tx has
// already committed are silently dropped. They cannot retroactively
// run, and silently dropping them prevents goroutines that race past
// the commit boundary from accumulating dead callbacks.
func TestOnCommit_RegistrationAfterTerminalIsNoop(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	ctx := PrepareTxCallbacks(context.Background())
	var leaked atomic.Bool

	// Capture the *TxCallbacks during the closure so we can attempt
	// to register against it after Transaction returns.
	var captured *TxCallbacks
	err := m.Transaction(ctx, func(txCtx context.Context) error {
		captured = lookupTxCallbacks(ctx)
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if captured == nil {
		t.Fatal("expected non-nil TxCallbacks during Transaction")
	}
	captured.OnCommit(func(c context.Context) error {
		leaked.Store(true)
		return nil
	})
	if leaked.Load() {
		t.Error("post-terminal OnCommit fired retroactively")
	}
}

// afterCommitModel implements AfterCommitHook to exercise the model
// lifecycle hook surface. The fired flag is reset between tests; we
// keep the recorder package-level because the model is constructed
// reflectively by the save path and we cannot pass test-scoped state
// through it.
type afterCommitModel struct {
	Model[afterCommitModel]
	Name string `orm:"column:name"`
}

func (afterCommitModel) TableName() string  { return "tx_hook_models" }
func (afterCommitModel) Fillable() []string { return []string{"name"} }

var afterCommitFired struct {
	mu       sync.Mutex
	commit   int
	rollback int
}

func (m *afterCommitModel) AfterCommit(ctx context.Context) error {
	afterCommitFired.mu.Lock()
	defer afterCommitFired.mu.Unlock()
	afterCommitFired.commit++
	return nil
}

func (m *afterCommitModel) AfterRollback(ctx context.Context) error {
	afterCommitFired.mu.Lock()
	defer afterCommitFired.mu.Unlock()
	afterCommitFired.rollback++
	return nil
}

func resetAfterCommitFired() {
	afterCommitFired.mu.Lock()
	defer afterCommitFired.mu.Unlock()
	afterCommitFired.commit = 0
	afterCommitFired.rollback = 0
}

// TestModel_AfterCommit_FiresOnCommitInsideTransaction verifies the
// model lifecycle hook AfterCommit fires when the model is created
// inside a Transaction that successfully commits. This is the
// motivating use case (outbox pattern, post-commit cache invalidation).
func TestModel_AfterCommit_FiresOnCommitInsideTransaction(t *testing.T) {
	_, cleanup := setupTxTest(t)
	defer cleanup()
	resetAfterCommitFired()

	ctx := PrepareTxCallbacks(context.Background())
	err := Default().Transaction(ctx, func(txCtx context.Context) error {
		_, err := (afterCommitModel{}).Create(txCtx, map[string]any{
			"name": "outbox-row",
		})
		return err
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	afterCommitFired.mu.Lock()
	defer afterCommitFired.mu.Unlock()
	if afterCommitFired.commit != 1 {
		t.Errorf("AfterCommit fire count = %d, want 1", afterCommitFired.commit)
	}
	if afterCommitFired.rollback != 0 {
		t.Errorf("AfterRollback fire count = %d, want 0", afterCommitFired.rollback)
	}
}

// TestModel_AfterRollback_FiresOnRollbackInsideTransaction is the
// symmetric test: a model whose enclosing Transaction rolls back
// must observe AfterRollback rather than AfterCommit.
func TestModel_AfterRollback_FiresOnRollbackInsideTransaction(t *testing.T) {
	_, cleanup := setupTxTest(t)
	defer cleanup()
	resetAfterCommitFired()

	ctx := PrepareTxCallbacks(context.Background())
	sentinel := errors.New("rollback")
	err := Default().Transaction(ctx, func(txCtx context.Context) error {
		if _, err := (afterCommitModel{}).Create(txCtx, map[string]any{
			"name": "rolled-back",
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction = %v, want sentinel", err)
	}
	afterCommitFired.mu.Lock()
	defer afterCommitFired.mu.Unlock()
	if afterCommitFired.commit != 0 {
		t.Errorf("AfterCommit fire count = %d, want 0 (tx rolled back)", afterCommitFired.commit)
	}
	if afterCommitFired.rollback != 1 {
		t.Errorf("AfterRollback fire count = %d, want 1", afterCommitFired.rollback)
	}
}

// TestModel_AfterCommit_FiresInlineWithoutTransaction verifies that
// when no Transaction surrounds the save, the AfterCommit hook still
// fires (inline) so the model's contract is uniform regardless of
// whether the caller was inside a tx. AfterRollback is not fired on
// the inline path: the implicit auto-commit cannot roll back.
func TestModel_AfterCommit_FiresInlineWithoutTransaction(t *testing.T) {
	_, cleanup := setupTxTest(t)
	defer cleanup()
	resetAfterCommitFired()

	// No PrepareTxCallbacks, no Transaction; Create auto-commits.
	_, err := (afterCommitModel{}).Create(context.Background(), map[string]any{"name": "inline"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	afterCommitFired.mu.Lock()
	defer afterCommitFired.mu.Unlock()
	if afterCommitFired.commit != 1 {
		t.Errorf("inline AfterCommit fire count = %d, want 1", afterCommitFired.commit)
	}
	if afterCommitFired.rollback != 0 {
		t.Errorf("inline AfterRollback fire count = %d, want 0 (no tx to roll back)", afterCommitFired.rollback)
	}
}

// TestModel_AfterCommit_PropagatesContextValues verifies the ctx
// reaching post-commit callbacks is the ctx wired into the
// surrounding Transaction (not a bare context.Background()). Without
// this, callbacks could not carry trace IDs, deadlines, or auth
// tokens through to the post-commit phase.
func TestModel_AfterCommit_PropagatesContextValues(t *testing.T) {
	_, cleanup := setupTxTest(t)
	defer cleanup()
	resetAfterCommitFired()

	type ctxKey string
	const k ctxKey = "trace"
	ctx := context.WithValue(context.Background(), k, "trace-abc")
	ctx = PrepareTxCallbacks(ctx)

	var observed string
	err := Default().Transaction(ctx, func(txCtx context.Context) error {
		// Register an OnCommit that captures the value so we can
		// assert without needing AfterCommit to write to package
		// state (which would race with the model hook above).
		return OnCommit(ctx, func(c context.Context) error {
			if v, ok := c.Value(k).(string); ok {
				observed = v
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if observed != "trace-abc" {
		t.Errorf("ctx value in commit callback = %q, want trace-abc (ctx not propagated)", observed)
	}
}

// TestOnCommit_PanicInClosureFiresRollback verifies the panic path:
// a panic inside the Transaction closure rolls back the tx AND drains
// rollback callbacks, then re-panics. Without this, callbacks could
// not fire on the most catastrophic failure mode.
func TestOnCommit_PanicInClosureFiresRollback(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	ctx := PrepareTxCallbacks(context.Background())
	var ran atomic.Bool

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic to propagate after Transaction recovered, drained, and re-raised")
		}
		if !ran.Load() {
			t.Error("OnRollback callback did not fire on panic path")
		}
	}()
	_ = m.Transaction(ctx, func(txCtx context.Context) error {
		_ = OnRollback(ctx, func(c context.Context) error {
			ran.Store(true)
			return nil
		})
		panic("boom")
	})
}

// TestOnCommitFailure_RunsForAmbiguousTx covers the commit-error
// path: when tx.Commit fails, the tx is in an AMBIGUOUS state (the
// database may have committed but the client did not see the OK).
// Running rollback hooks would corrupt outboxes and caches, so the
// wrapper must drain ONLY OnCommitFailure callbacks and leave the
// rollback list untouched.
//
// We exercise this by rolling back the *sql.Tx manually inside the
// closure; the subsequent tx.Commit returns sql.ErrTxDone, which is
// indistinguishable from a real driver-level commit failure for the
// purposes of the wrapper.
func TestOnCommitFailure_RunsForAmbiguousTx(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	ctx := PrepareTxCallbacks(context.Background())
	var commitRan atomic.Bool
	var rollbackRan atomic.Bool
	var commitFailureRan atomic.Bool
	var observedErr error

	err := m.Transaction(ctx, func(txCtx context.Context) error {
		_ = OnCommit(ctx, func(c context.Context) error {
			commitRan.Store(true)
			return nil
		})
		_ = OnRollback(ctx, func(c context.Context) error {
			rollbackRan.Store(true)
			return nil
		})
		_ = OnCommitFailure(ctx, func(c context.Context, cmErr error) error {
			commitFailureRan.Store(true)
			observedErr = cmErr
			return nil
		})
		// Roll back the tx out from under the wrapper so the
		// subsequent tx.Commit returns sql.ErrTxDone. Returning nil
		// from the closure lets the wrapper attempt the commit.
		tx, _ := TxFromContext(txCtx)
		if rbErr := tx.Rollback(); rbErr != nil {
			return rbErr
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected commit error after pre-emptive Rollback, got nil")
	}
	if commitRan.Load() {
		t.Error("OnCommit fired despite tx.Commit failure")
	}
	if rollbackRan.Load() {
		t.Error("OnRollback fired on commit-error path; ambiguous tx must not run rollback hooks")
	}
	if !commitFailureRan.Load() {
		t.Error("OnCommitFailure did not fire on tx.Commit failure")
	}
	if observedErr == nil {
		t.Error("OnCommitFailure callback did not receive commit error argument")
	}
	if !errors.Is(observedErr, sql.ErrTxDone) {
		t.Errorf("OnCommitFailure observed err = %v, want sql.ErrTxDone", observedErr)
	}
}

// TestHookCtx_SurvivesCancellation verifies fix B2: hooks must not
// inherit cancellation from the parent ctx. A canceled parent ctx
// (e.g. the request that drove the commit timing out mid-Commit)
// must not poison post-commit / post-rollback callbacks. The hook
// runs against a context.WithoutCancel-derived ctx so the same
// values (trace IDs, etc.) flow through but Done() never fires.
func TestHookCtx_SurvivesCancellation(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	type ctxKey string
	const k ctxKey = "trace"
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), k, "trace-survived"))
	ctx := PrepareTxCallbacks(parent)

	var ran atomic.Bool
	var observedDone atomic.Bool
	var observedValue string

	err := m.Transaction(ctx, func(txCtx context.Context) error {
		return OnCommit(ctx, func(c context.Context) error {
			// Cancel the parent ctx from inside the callback so the
			// callback ctx and the parent ctx share lifecycle if
			// WithoutCancel is NOT applied. With WithoutCancel applied,
			// the callback ctx Done() must remain unsignaled even
			// after the parent is canceled.
			cancel()
			select {
			case <-c.Done():
				observedDone.Store(true)
			default:
			}
			if v, ok := c.Value(k).(string); ok {
				observedValue = v
			}
			ran.Store(true)
			return nil
		})
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if !ran.Load() {
		t.Fatal("OnCommit callback did not fire")
	}
	if observedDone.Load() {
		t.Error("hook ctx Done() fired when parent was canceled; WithoutCancel detaching is broken")
	}
	if observedValue != "trace-survived" {
		t.Errorf("hook ctx value = %q, want trace-survived (parent values must propagate)", observedValue)
	}
}

// TestPanicInHookFiresTxRecoverEvent verifies fix B3: a hook panic
// fires a TxRecover event through the manager's dispatcher even when
// the manager's logger is nil. Without this, panics inside post-commit
// hooks were silently swallowed when no logger was wired.
func TestPanicInHookFiresTxRecoverEvent(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	// Explicitly leave the logger unset so the dispatcher is the only
	// observability sink for the panic.
	var captured atomic.Value
	m.SetEventDispatcher(func(_ context.Context, ev any) error {
		if rec, ok := ev.(*TxRecover); ok {
			captured.Store(rec)
		}
		return nil
	})

	ctx := PrepareTxCallbacks(context.Background())
	err := m.Transaction(ctx, func(txCtx context.Context) error {
		return OnCommit(ctx, func(c context.Context) error {
			panic("boom inside callback")
		})
	})
	if err != nil {
		t.Fatalf("Transaction: %v (panic in post-commit hook must not surface to caller)", err)
	}
	got := captured.Load()
	if got == nil {
		t.Fatal("TxRecover event was not dispatched after callback panic")
	}
	rec, ok := got.(*TxRecover)
	if !ok {
		t.Fatalf("dispatched event = %T, want *TxRecover", got)
	}
	if rec.Cause != "callback_panic" {
		t.Errorf("TxRecover.Cause = %q, want %q", rec.Cause, "callback_panic")
	}
	if rec.PanicValue == "" {
		t.Error("TxRecover.PanicValue is empty; expected panic message captured")
	}
}

// panickingAfterCommitModel implements AfterCommitHook with a panicking
// AfterCommit so the inline (no-Transaction, auto-commit) registration
// path's panic-recovery surface can be witnessed.
type panickingAfterCommitModel struct {
	Model[panickingAfterCommitModel]
	Name string
}

func (panickingAfterCommitModel) TableName() string  { return "tx_hook_models" }
func (panickingAfterCommitModel) Fillable() []string { return []string{"name"} }

func (m *panickingAfterCommitModel) AfterCommit(ctx context.Context) error {
	panic("inline AfterCommit boom")
}

// TestModel_InlineAfterCommit_PanicFiresTxRecoverEvent pins the
// residual fix from Phase B re-review: a panic inside an AfterCommit
// hook fired via the inline (auto-commit, no Transaction wrapper) path
// must dispatch a TxRecover event through Manager.dispatchEvent.
//
// Pre-fix the inline branch passed nil dispatcher to runCallbackSafe,
// so the panic landed only on os.Stderr and the observability pipe wired
// to *Manager never saw it. The fix stamps the dispatcher onto ctx so
// the inline path can route the panic through the same TxRecover stream
// the in-Transaction path uses.
func TestModel_InlineAfterCommit_PanicFiresTxRecoverEvent(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	var captured atomic.Value
	m.SetEventDispatcher(func(_ context.Context, ev any) error {
		if rec, ok := ev.(*TxRecover); ok {
			captured.Store(rec)
		}
		return nil
	})

	// No PrepareTxCallbacks, no Transaction; Create auto-commits and
	// the AfterCommit hook fires inline via registerModelAfterCommit.
	_, err := (panickingAfterCommitModel{}).Create(context.Background(), map[string]any{"name": "inline-panic"})
	if err != nil {
		t.Fatalf("Create returned error (panic in inline AfterCommit must not surface): %v", err)
	}

	got := captured.Load()
	if got == nil {
		t.Fatal("TxRecover event was not dispatched after inline AfterCommit panic")
	}
	rec, ok := got.(*TxRecover)
	if !ok {
		t.Fatalf("dispatched event = %T, want *TxRecover", got)
	}
	if rec.Cause != "callback_panic" {
		t.Errorf("TxRecover.Cause = %q, want %q", rec.Cause, "callback_panic")
	}
	if rec.PanicValue == "" {
		t.Error("TxRecover.PanicValue is empty; expected panic message captured")
	}
}
