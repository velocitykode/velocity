package orm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// levelLog records every entry with its level, Debug included.
type levelLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *levelLog) Debug(msg string, _ ...any) { l.add("DEBUG " + msg) }
func (l *levelLog) Info(msg string, _ ...any)  { l.add("INFO " + msg) }
func (l *levelLog) Warn(msg string, _ ...any)  { l.add("WARN " + msg) }
func (l *levelLog) Error(msg string, _ ...any) { l.add("ERROR " + msg) }
func (l *levelLog) Fatal(msg string, _ ...any) { l.add("FATAL " + msg) }

func (l *levelLog) add(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
}

func (l *levelLog) count(prefix string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.entries {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

// rollBackThenPanic is a Transaction body whose own rollback makes the
// manager's rollback after the panic fail.
func rollBackThenPanic(ctx context.Context) error {
	tx, ok := TxFromContext(ctx)
	if !ok {
		panic("no transaction on ctx")
	}
	_ = tx.Rollback()
	panic("body blew up")
}

// runRecovering runs fn and swallows the panic Transaction re-raises.
func runRecovering(fn func()) {
	defer func() { _ = recover() }()
	fn()
}

// A rollback that fails after a panic is one error line: through the
// manager's logger when it has one, through the one framework fallback
// when it has none, and never through the standard library log package or
// slog.Default.
func TestTransaction_RollbackFailureAfterPanicIsOneLine(t *testing.T) {
	const msg = "velocity/orm: rollback failed after panic"
	t.Run("manager logger", func(t *testing.T) {
		m := newTestManager(t)
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
		logs := &levelLog{}
		m.SetLogger(logs)

		runRecovering(func() { _ = m.Transaction(context.Background(), rollBackThenPanic) })

		if got := logs.count("ERROR " + msg); got != 1 {
			t.Errorf("manager logger lines = %d, want 1: %v", got, logs.entries)
		}
	})
	t.Run("no logger", func(t *testing.T) {
		fallback := fallbacklogtest.Capture(t)
		stdlib := fallbacklogtest.CaptureStdlib(t)
		m := newTestManager(t)
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

		runRecovering(func() { _ = m.Transaction(context.Background(), rollBackThenPanic) })

		if got := fallback.Count("ERROR", msg); got != 1 {
			t.Errorf("fallback lines = %d, want 1: %q", got, fallback.String())
		}
		if out := stdlib.String(); out != "" {
			t.Errorf("stdlib log / slog.Default got %q, want nothing", out)
		}
	})
}

// A tx callback that panics or fails with no logger writes through the
// fallback, whether or not a TxRecover dispatcher is wired.
func TestRunCallbackSafe_WithoutLoggerWritesThroughTheFallback(t *testing.T) {
	for _, tc := range []struct {
		name     string
		dispatch func(*TxRecover)
	}{
		{"no dispatcher", nil},
		{"dispatcher", func(*TxRecover) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fallback := fallbacklogtest.Capture(t)

			runCallbackSafe(context.Background(), func(context.Context) error { panic("hook boom") }, "after_commit", nil, tc.dispatch)
			runCallbackSafe(context.Background(), func(context.Context) error { return errors.New("hook failed") }, "after_commit", nil, tc.dispatch)

			if got := fallback.Count("ERROR", "velocity/orm: tx callback panicked"); got != 1 {
				t.Errorf("panic lines = %d, want 1: %q", got, fallback.String())
			}
			if got := fallback.Count("WARN", "velocity/orm: tx callback returned error"); got != 1 {
				t.Errorf("error lines = %d, want 1: %q", got, fallback.String())
			}
		})
	}
}

// An AfterCommit hook that panics on the inline (auto-commit) path writes
// through the manager's logger, like one fired by a Transaction.
func TestSave_InlineAfterCommitPanicWritesThroughTheManagerLogger(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()
	logs := &levelLog{}
	m.SetLogger(logs)

	if _, err := (panickingAfterCommitModel{}).Create(context.Background(), map[string]any{"name": "inline"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got := logs.count("ERROR velocity/orm: tx callback panicked"); got != 1 {
		t.Errorf("manager logger lines = %d, want 1: %v", got, logs.entries)
	}
}

// The manager hands its logger to its connections' query logger, those it
// holds when SetLogger runs and those added later.
func TestManagerSetLogger_ReachesTheQueryLogger(t *testing.T) {
	m, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:", LogQueries: true})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	logs := &levelLog{}
	m.SetLogger(logs)

	if _, err := m.Exec(context.Background(), "CREATE TABLE logged (id INTEGER)"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got := logs.count("DEBUG velocity/orm: query executed"); got != 1 {
		t.Errorf("default connection query lines = %d, want 1: %v", got, logs.entries)
	}

	other, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:", LogQueries: true})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = other.Shutdown(context.Background()) })
	drv, err := other.liveDriver()
	if err != nil {
		t.Fatalf("liveDriver: %v", err)
	}
	m.AddConnection("reports", drv)
	if _, err := drv.ExecContext(context.Background(), "CREATE TABLE reports (id INTEGER)"); err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if got := logs.count("DEBUG velocity/orm: query executed"); got != 2 {
		t.Errorf("query lines after AddConnection = %d, want 2: %v", got, logs.entries)
	}
}

// A relay without a logger of its own writes through its manager's logger,
// or the fallback when the manager has none; its own logger wins.
func TestRelay_LoggerDefaultsToItsManagerLogger(t *testing.T) {
	m := &Manager{}
	r := NewRelay(m, RelayCallbacks{}, RelayConfig{})
	if _, ok := r.log().(fallbacklog.Logger); !ok {
		t.Errorf("no loggers: r.log() = %T, want fallbacklog.Logger", r.log())
	}
	managerLog := &levelLog{}
	m.SetLogger(managerLog)
	if r.log() != contract.Logger(managerLog) {
		t.Errorf("manager logger: r.log() = %T, want the manager's logger", r.log())
	}
	own := &levelLog{}
	r.SetLogger(own)
	if r.log() != contract.Logger(own) {
		t.Errorf("own logger: r.log() = %T, want the relay's logger", r.log())
	}
	r.SetLogger(nil)
	if r.log() != contract.Logger(managerLog) {
		t.Errorf("after SetLogger(nil): r.log() = %T, want the manager's logger", r.log())
	}
}

// SetLogger may run while the relay loop is running and logging. Run under
// -race: the loop fails every claim (no outbox table) and logs each one.
func TestRelay_SetLoggerWhileRunningIsSafe(t *testing.T) {
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	r := NewRelay(m, RelayCallbacks{}, RelayConfig{PollInterval: time.Millisecond})
	r.SetLogger(&levelLog{})
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for i := 0; i < 50; i++ {
		r.SetLogger(&levelLog{})
		time.Sleep(time.Millisecond)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
