package orm

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/trace"
)

const (
	reqID   = "req-orm-1"
	traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	spanID  = "00f067aa0ba902b7"
)

func requestCtx() context.Context {
	return trace.WithTrace(trace.WithRequestID(context.Background(), reqID), traceID, spanID)
}

// assertRequestIDs fails unless e carries the request and trace ids of
// requestCtx and a span id.
func assertRequestIDs(t *testing.T, e kvEntry) {
	t.Helper()
	if got := e.value("request_id"); got != reqID {
		t.Errorf("request_id = %v, want %q (%s)", got, reqID, e)
	}
	if got := e.value("trace_id"); got != traceID {
		t.Errorf("trace_id = %v, want %q (%s)", got, traceID, e)
	}
	if got, _ := e.value("span_id").(string); got == "" {
		t.Errorf("span_id missing (%s)", e)
	}
}

// A request-time transaction callback failure, and a rollback failure,
// name the request they happened under.
func TestORMDiagnostics_CarryTheRequestIDs(t *testing.T) {
	t.Run("tx callback", func(t *testing.T) {
		logs := &kvRecorder{}
		runCallbackSafe(requestCtx(), func(context.Context) error { return errors.New("cb failed") }, "after_commit", logs, nil)
		runCallbackSafe(requestCtx(), func(context.Context) error { panic("cb boom") }, "after_commit", logs, nil)
		for _, msg := range []string{"velocity/orm: tx callback returned error", "velocity/orm: tx callback panicked"} {
			lines := logs.find(msg)
			if len(lines) != 1 {
				t.Fatalf("%q lines = %v, want 1", msg, logs.entries)
			}
			assertRequestIDs(t, lines[0])
		}
	})
	t.Run("tx callback without a ctx", func(t *testing.T) {
		logs := &kvRecorder{}
		var noCtx context.Context // a nil ctx must not break the line
		runCallbackSafe(noCtx, func(context.Context) error { return errors.New("cb failed") }, "after_commit", logs, nil)
		lines := logs.find("velocity/orm: tx callback returned error")
		if len(lines) != 1 || lines[0].value("request_id") != nil {
			t.Errorf("lines = %v, want one line without ids", logs.entries)
		}
	})
	t.Run("rollback", func(t *testing.T) {
		m := newTestManager(t)
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
		logs := &kvRecorder{}
		m.SetLogger(logs)
		_ = m.Transaction(requestCtx(), func(ctx context.Context) error {
			tx, _ := TxFromContext(ctx)
			_ = tx.Rollback()
			return errors.New("body failed")
		})
		runRecovering(func() { _ = m.Transaction(requestCtx(), rollBackThenPanic) })
		for _, msg := range []string{"velocity/orm: rollback failed", "velocity/orm: rollback failed after panic"} {
			lines := logs.find(msg)
			if len(lines) != 1 {
				t.Fatalf("%q lines = %v, want 1", msg, logs.entries)
			}
			assertRequestIDs(t, lines[0])
		}
	})
	t.Run("outbox rollback", func(t *testing.T) {
		m := newTestManager(t)
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
		logs := &kvRecorder{}
		m.SetLogger(logs)
		_ = m.TransactionWithOutbox(requestCtx(), func(tx *sql.Tx, _ Pending) error {
			_ = tx.Rollback()
			return errors.New("body failed")
		})
		_ = m.TransactionWithOutbox(requestCtx(), func(tx *sql.Tx, _ Pending) error {
			_ = tx.Rollback()
			panic("body blew up")
		})
		for _, msg := range []string{"velocity/orm: rollback failed in outbox tx", "velocity/orm: rollback failed after panic in outbox tx"} {
			lines := logs.find(msg)
			if len(lines) != 1 {
				t.Fatalf("%q lines = %v, want 1", msg, logs.entries)
			}
			assertRequestIDs(t, lines[0])
		}
	})
	t.Run("drained callback", func(t *testing.T) {
		m := newTestManager(t)
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
		logs := &kvRecorder{}
		m.SetLogger(logs)
		ctx := PrepareTxCallbacks(requestCtx())
		_ = m.Transaction(ctx, func(ctx context.Context) error {
			OnCommit(ctx, func(context.Context) error { return errors.New("on commit failed") })
			return nil
		})
		lines := logs.find("velocity/orm: tx callback returned error")
		if len(lines) != 1 {
			t.Fatalf("callback lines = %v, want 1", logs.entries)
		}
		assertRequestIDs(t, lines[0])
	})
}
