package drivers

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

type queryLogEntry struct {
	level string
	msg   string
	kvs   []any
}

// queryLog is a contract.Logger that records every entry with its level.
type queryLog struct {
	mu      sync.Mutex
	entries []queryLogEntry
}

func (l *queryLog) Debug(msg string, kvs ...any) { l.add("debug", msg, kvs) }
func (l *queryLog) Info(msg string, kvs ...any)  { l.add("info", msg, kvs) }
func (l *queryLog) Warn(msg string, kvs ...any)  { l.add("warn", msg, kvs) }
func (l *queryLog) Error(msg string, kvs ...any) { l.add("error", msg, kvs) }
func (l *queryLog) Fatal(msg string, kvs ...any) { l.add("fatal", msg, kvs) }

func (l *queryLog) add(level, msg string, kvs []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, queryLogEntry{level: level, msg: msg, kvs: kvs})
}

func (l *queryLog) all() []queryLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]queryLogEntry(nil), l.entries...)
}

// captureStdout returns what fn wrote to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	func() {
		defer func() { os.Stdout = orig }()
		fn()
	}()
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return out.String()
}

func connectLoggingSQLite(t *testing.T) Driver {
	t.Helper()
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxOpenConns: 1, LogQueries: true}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	return driver
}

// With a logger set and LogQueries on, each executed statement is one
// debug line with the statement and its argument count, and nothing
// reaches stdout.
func TestSQLiteDriverSetLogger_WritesStatementsAtDebug(t *testing.T) {
	driver := connectLoggingSQLite(t)
	log := &queryLog{}
	driver.(contract.LoggerAware).SetLogger(log)

	ctx := context.Background()
	out := captureStdout(t, func() {
		if _, err := driver.ExecContext(ctx, "CREATE TABLE logged (id INTEGER)"); err != nil {
			t.Errorf("create: %v", err)
		}
		rows, err := driver.QueryContext(ctx, "SELECT id FROM logged WHERE id = ?", 7)
		if err != nil {
			t.Errorf("select: %v", err)
			return
		}
		_ = rows.Close()
	})

	if out != "" {
		t.Errorf("stdout = %q, want nothing once a logger is set", out)
	}
	want := []queryLogEntry{
		{level: "debug", msg: "velocity/orm: query executed", kvs: []any{"query", "CREATE TABLE logged (id INTEGER)", "arg_count", 0}},
		{level: "debug", msg: "velocity/orm: query executed", kvs: []any{"query", "SELECT id FROM logged WHERE id = ?", "arg_count", 1}},
	}
	got := log.all()
	if len(got) != len(want) {
		t.Fatalf("entries = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].level != want[i].level || got[i].msg != want[i].msg || !sameKVs(got[i].kvs, want[i].kvs) {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Without a logger (never set, or reset with nil) a statement goes to
// stdout in the historical format; with LogQueries off nothing is written.
func TestBaseDriverLogQuery_WithoutLogger(t *testing.T) {
	tests := []struct {
		name       string
		logQueries bool
		reset      bool
		want       string
	}{
		{name: "never set", logQueries: true, want: "SQL: SELECT 1\nArgs: [2 params]\n"},
		{name: "reset to nil", logQueries: true, reset: true, want: "SQL: SELECT 1\nArgs: [2 params]\n"},
		{name: "query logging off", logQueries: false, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &BaseDriver{Config: ConnectionConfig{LogQueries: tt.logQueries}}
			log := &queryLog{}
			if tt.reset {
				b.SetLogger(log)
				b.SetLogger(nil)
			}
			if got := captureStdout(t, func() { b.logQuery("SELECT 1", 2) }); got != tt.want {
				t.Errorf("stdout = %q, want %q", got, tt.want)
			}
			if n := len(log.all()); n != 0 {
				t.Errorf("logger got %d entries after reset, want 0", n)
			}
		})
	}
}

func sameKVs(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
