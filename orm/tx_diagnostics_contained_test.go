package orm

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// panickingQueryLogger panics on every line.
type panickingQueryLogger struct{}

func (panickingQueryLogger) Debug(string, ...any)              { panic("query logger boom") }
func (panickingQueryLogger) Info(string, ...any)               { panic("query logger boom") }
func (panickingQueryLogger) Warn(string, ...any)               { panic("query logger boom") }
func (panickingQueryLogger) Error(string, ...any)              { panic("query logger boom") }
func (panickingQueryLogger) Fatal(string, ...any)              { panic("query logger boom") }
func (p panickingQueryLogger) With(kvs ...any) contract.Logger { return contract.BindFields(p, kvs...) }

// txRecoverLog records the TxRecover events a manager dispatches.
type txRecoverLog struct {
	mu     sync.Mutex
	causes []string
}

func (l *txRecoverLog) dispatch(_ context.Context, ev any) error {
	if r, ok := ev.(*TxRecover); ok {
		l.mu.Lock()
		l.causes = append(l.causes, r.Cause)
		l.mu.Unlock()
	}
	return nil
}

func (l *txRecoverLog) count(cause string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, c := range l.causes {
		if c == cause {
			n++
		}
	}
	return n
}

// panickingLoggerTxManager returns a manager whose logger panics on every
// line, dispatching into a TxRecover log.
func panickingLoggerTxManager(t *testing.T) (*Manager, *txRecoverLog) {
	t.Helper()
	fallbacklogtest.Capture(t)
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	m.SetLogger(panickingQueryLogger{})
	events := &txRecoverLog{}
	m.SetEventDispatcher(events.dispatch)
	return m, events
}

// A commit callback that panics, or returns an error, is reported without
// stopping the callbacks after it, even when the logger panics on the
// report: the transaction commits, every later callback runs, and only a
// panic is dispatched as a callback panic.
func TestTxCallbacks_PanickingLoggerDoesNotStopSiblings(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failing    TxCallback
		wantEvents int
	}{
		{"panicking callback", func(context.Context) error { panic("callback boom") }, 1},
		{"failing callback", func(context.Context) error { return errors.New("callback failed") }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, events := panickingLoggerTxManager(t)
			ran := false
			err := func() (err error) {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("a panic escaped the committed transaction: %v", p)
					}
				}()
				ctx := PrepareTxCallbacks(context.Background())
				return m.Transaction(ctx, func(ctx context.Context) error {
					if err := OnCommit(ctx, tc.failing); err != nil {
						return err
					}
					return OnCommit(ctx, func(context.Context) error { ran = true; return nil })
				})
			}()
			if err != nil {
				t.Fatalf("Transaction: %v", err)
			}
			if !ran {
				t.Error("the callback after the failing one did not run")
			}
			if got := events.count("callback_panic"); got != tc.wantEvents {
				t.Errorf("callback_panic events = %d, want %d", got, tc.wantEvents)
			}
		})
	}
}

// commitInside commits the transaction fn runs in, so the rollback the
// Transaction attempts afterwards fails.
func commitInside(ctx context.Context) {
	if tx, ok := TxFromContext(ctx); ok {
		_ = tx.Commit()
	}
}

// A failed rollback is reported without letting a panicking logger change
// the outcome: fn's error is returned, the rollback callbacks run and the
// TxRecover event is dispatched.
func TestTransaction_PanickingLoggerOnRollbackFailure(t *testing.T) {
	m, events := panickingLoggerTxManager(t)
	want := errors.New("fn failed")
	rolledBack := false
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("the rollback-failure line panicked out of Transaction: %v", p)
			}
		}()
		ctx := PrepareTxCallbacks(context.Background())
		err = m.Transaction(ctx, func(ctx context.Context) error {
			_ = OnRollback(ctx, func(context.Context) error { rolledBack = true; return nil })
			commitInside(ctx)
			return want
		})
	}()
	if !errors.Is(err, want) {
		t.Errorf("Transaction = %v, want fn's error", err)
	}
	if !rolledBack {
		t.Error("rollback callbacks did not run")
	}
	if got := events.count("error"); got != 1 {
		t.Errorf("TxRecover error events = %d, want 1", got)
	}
}

// On a panic whose rollback fails, the panic Transaction re-raises is fn's
// own, not the logger's, and the rollback callbacks and the TxRecover
// event run first.
func TestTransaction_PanickingLoggerOnPanicRollbackFailure(t *testing.T) {
	m, events := panickingLoggerTxManager(t)
	rolledBack := false
	var got any
	func() {
		defer func() { got = recover() }()
		ctx := PrepareTxCallbacks(context.Background())
		_ = m.Transaction(ctx, func(ctx context.Context) error {
			_ = OnRollback(ctx, func(context.Context) error { rolledBack = true; return nil })
			commitInside(ctx)
			panic("fn boom")
		})
	}()
	if got != "fn boom" {
		t.Errorf("re-raised panic = %v, want fn's own", got)
	}
	if !rolledBack {
		t.Error("rollback callbacks did not run")
	}
	if n := events.count("panic"); n != 1 {
		t.Errorf("TxRecover panic events = %d, want 1", n)
	}
}

// TransactionWithOutbox keeps its contract, a panic becomes an error, when
// the logger reporting the failed rollback panics too.
func TestTransactionWithOutbox_PanickingLoggerOnRollbackFailure(t *testing.T) {
	m, _ := newOutboxFileManager(t)
	fallbacklogtest.Capture(t)
	m.SetLogger(panickingQueryLogger{})
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("a panic escaped TransactionWithOutbox: %v", p)
			}
		}()
		err = m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, _ Pending) error {
			_ = tx.Commit()
			panic("fn boom")
		})
	}()
	if err == nil {
		t.Error("TransactionWithOutbox = nil, want the panic as an error")
	}
}
