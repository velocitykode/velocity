package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
)

// kvEntry is one recorded log call.
type kvEntry struct {
	level string
	msg   string
	kvs   []any
}

// kvRecorder records every call with its key-value pairs, bound fields
// first.
type kvRecorder struct {
	mu      sync.Mutex
	entries []kvEntry
}

func (l *kvRecorder) add(level, msg string, kvs []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, kvEntry{level, msg, append([]any(nil), kvs...)})
}

func (l *kvRecorder) Debug(msg string, kvs ...any)    { l.add("debug", msg, kvs) }
func (l *kvRecorder) Info(msg string, kvs ...any)     { l.add("info", msg, kvs) }
func (l *kvRecorder) Warn(msg string, kvs ...any)     { l.add("warn", msg, kvs) }
func (l *kvRecorder) Error(msg string, kvs ...any)    { l.add("error", msg, kvs) }
func (l *kvRecorder) Fatal(msg string, kvs ...any)    { l.add("fatal", msg, kvs) }
func (l *kvRecorder) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }

// find returns the entries whose message is msg.
func (l *kvRecorder) find(msg string) []kvEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []kvEntry
	for _, e := range l.entries {
		if e.msg == msg {
			out = append(out, e)
		}
	}
	return out
}

// value returns the value logged under key, or nil.
func (e kvEntry) value(key string) any {
	for i := 0; i+1 < len(e.kvs); i += 2 {
		if e.kvs[i] == key {
			return e.kvs[i+1]
		}
	}
	return nil
}

func (e kvEntry) String() string {
	return e.level + " " + e.msg + " " + strings.TrimSpace(fmt.Sprintln(e.kvs...))
}

// echoedSecret stands in for a value a driver echoes in its error text.
const echoedSecret = "hunter2@example.com"

// errEchoesValue fails the way a driver does on a rejected value.
var errEchoesValue = errors.New(`duplicate key value violates unique constraint: Key (email)=(` + echoedSecret + `)`)

// assertKindOnly fails unless e names the error by kind under each key in
// kinds, has no raw error key, and carries no echoed value.
func assertKindOnly(t *testing.T, e kvEntry, kinds map[string]string) {
	t.Helper()
	if s := e.String(); strings.Contains(s, echoedSecret) || strings.Contains(s, "duplicate key") {
		t.Errorf("line carries the driver's error text: %s", s)
	}
	for _, k := range []string{"error", "original_error"} {
		if v := e.value(k); v != nil {
			t.Errorf("line logs %s=%v, want only its kind", k, v)
		}
	}
	for k, want := range kinds {
		if got := e.value(k); got != want {
			t.Errorf("%s = %v, want %q (%s)", k, got, want, e)
		}
	}
}

// A failed rollback logs the rollback error and the body's error by kind,
// never their text: a driver error echoes the rejected value. The body's
// error still reaches the caller unchanged.
func TestTransaction_RollbackFailureLogsErrorKindsOnly(t *testing.T) {
	t.Run("body error", func(t *testing.T) {
		m := newTestManager(t)
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
		logs := &kvRecorder{}
		m.SetLogger(logs)

		err := m.Transaction(context.Background(), func(ctx context.Context) error {
			tx, _ := TxFromContext(ctx)
			_ = tx.Rollback()
			return fmt.Errorf("insert: %w", errEchoesValue)
		})
		if !errors.Is(err, errEchoesValue) {
			t.Fatalf("Transaction error = %v, want the body's error", err)
		}
		lines := logs.find("velocity/orm: rollback failed")
		if len(lines) != 1 {
			t.Fatalf("rollback lines = %v, want 1", logs.entries)
		}
		assertKindOnly(t, lines[0], map[string]string{"error_kind": "sql.ErrTxDone", "original_error_kind": "*errors.errorString"})
	})
	t.Run("panic", func(t *testing.T) {
		m := newTestManager(t)
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
		logs := &kvRecorder{}
		m.SetLogger(logs)

		runRecovering(func() { _ = m.Transaction(context.Background(), rollBackThenPanic) })

		lines := logs.find("velocity/orm: rollback failed after panic")
		if len(lines) != 1 {
			t.Fatalf("rollback lines = %v, want 1", logs.entries)
		}
		assertKindOnly(t, lines[0], map[string]string{"error_kind": "sql.ErrTxDone"})
	})
}

// The outbox transaction's rollback lines follow the same rule.
func TestTransactionWithOutbox_RollbackFailureLogsErrorKindsOnly(t *testing.T) {
	t.Run("body error", func(t *testing.T) {
		m := newTestManager(t)
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
		logs := &kvRecorder{}
		m.SetLogger(logs)

		err := m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, _ Pending) error {
			_ = tx.Rollback()
			return errEchoesValue
		})
		if !errors.Is(err, errEchoesValue) {
			t.Fatalf("TransactionWithOutbox error = %v, want the body's error", err)
		}
		lines := logs.find("velocity/orm: rollback failed in outbox tx")
		if len(lines) != 1 {
			t.Fatalf("rollback lines = %v, want 1", logs.entries)
		}
		assertKindOnly(t, lines[0], map[string]string{"error_kind": "sql.ErrTxDone", "original_error_kind": "*errors.errorString"})
	})
	t.Run("panic", func(t *testing.T) {
		m := newTestManager(t)
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
		logs := &kvRecorder{}
		m.SetLogger(logs)

		_ = m.TransactionWithOutbox(context.Background(), func(tx *sql.Tx, _ Pending) error {
			_ = tx.Rollback()
			panic("body blew up")
		})

		lines := logs.find("velocity/orm: rollback failed after panic in outbox tx")
		if len(lines) != 1 {
			t.Fatalf("rollback lines = %v, want 1", logs.entries)
		}
		assertKindOnly(t, lines[0], map[string]string{"error_kind": "sql.ErrTxDone"})
	})
}

// A relay whose claim query fails logs the failure by kind only.
func TestRelay_ClaimFailureLogsErrorKindOnly(t *testing.T) {
	m := newTestManager(t) // no outbox table: the claim query fails
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	logs := &kvRecorder{}
	r := NewRelay(m, RelayCallbacks{}, RelayConfig{})
	r.SetLogger(logs)

	r.tick(context.Background(), make(chan struct{}, 1))

	lines := logs.find("velocity/orm: relay claim batch failed")
	if len(lines) != 1 {
		t.Fatalf("claim lines = %v, want 1", logs.entries)
	}
	e := lines[0]
	if v := e.value("error"); v != nil {
		t.Errorf("line logs error=%v, want only its kind", v)
	}
	if k, _ := e.value("error_kind").(string); k == "" {
		t.Errorf("line %s has no error_kind", e)
	}
}

// ManagerConfig.Logger is the manager's logger from construction on: the
// statements its driver runs while it connects write to it, and so do the
// manager's own lines. Nil leaves the fallback in place.
func TestNewManager_ConfigLoggerTakesTheConnectStatements(t *testing.T) {
	logs := &kvRecorder{}
	m, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:", LogQueries: true, Logger: logs})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	if n := len(logs.find("velocity/orm: query executed")); n != 2 {
		t.Errorf("connect statement lines = %d, want 2 (the PRAGMAs): %+v", n, logs.entries)
	}
	if m.Logger() != contract.Logger(logs) {
		t.Errorf("Logger() = %T, want the config logger", m.Logger())
	}

	bare, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:"})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = bare.Shutdown(context.Background()) })
	if _, ok := bare.Logger().(fallbacklog.Logger); !ok {
		t.Errorf("nil config logger: Logger() = %T, want the fallback", bare.Logger())
	}
}
