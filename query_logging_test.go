package velocity

import (
	"context"
	"database/sql/driver"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/trace"
)

// queryArgValue is a bound value no log line may carry.
const queryArgValue = "hunter2-bound-secret"

// queryTraceID is the trace the slow query runs under.
const queryTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

var registerRootSleepOnce sync.Once

// registerRootSleep installs sleep_ms(n) on every SQLite connection opened
// afterwards: it holds the statement for n milliseconds.
func registerRootSleep() {
	registerRootSleepOnce.Do(func() {
		sqlite.MustRegisterScalarFunction("sleep_ms", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			ms, _ := args[0].(int64)
			time.Sleep(time.Duration(ms) * time.Millisecond)
			return ms, nil
		})
	})
}

// newQueryLoggingApp builds an app on in-memory SQLite whose DB settings
// come from the environment (ConfigFromEnv), logging through the file
// driver at debug into dir.
func newQueryLoggingApp(t *testing.T, dir string, opts ...Option) *App {
	t.Helper()
	registerRootSleep()
	env := ConfigFromEnv()
	db := DBConfig{Connection: "sqlite", Database: ":memory:", MaxOpenConns: 1, LogQueries: env.DB.LogQueries, SlowThreshold: env.DB.SlowThreshold}
	a, err := New(append([]Option{WithConfig(Config{
		Env:   "testing",
		Port:  "0",
		Log:   log.LogConfig{Driver: "file", Config: map[string]any{"path": dir, "level": "debug"}},
		DB:    db,
		Cache: CacheConfig{Driver: "memory"},
		Queue: QueueConfig{Driver: "memory"},
		Mail:  mail.MailConfig{Driver: "log"},
	})}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	return a
}

// readLogFile returns the one log file the file driver wrote in dir.
func readLogFile(t *testing.T, dir string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "velocity-*.log"))
	if err != nil || len(files) != 1 {
		t.Fatalf("log files = %v (%v), want one", files, err)
	}
	content, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	return string(content)
}

// linesWith returns the lines of content holding fragment.
func linesWith(content, fragment string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, fragment) {
			out = append(out, line)
		}
	}
	return out
}

// With DB_SLOW_QUERY_THRESHOLD=100ms, a 150ms query writes one warn line
// (statement, duration, trace id, no argument values) and its
// QueryExecuted says Slow; a 50ms query writes none and is not Slow.
func TestDBSlowQueryThreshold_WarnsOnceAndMarksTheEvent(t *testing.T) {
	t.Setenv("DB_SLOW_QUERY_THRESHOLD", "100ms")
	t.Setenv("DB_LOG_QUERIES", "false")
	dir := t.TempDir()
	fake := events.NewFakeDispatcher()
	a := newQueryLoggingApp(t, dir, WithFakeEvents(fake))

	ctx := trace.WithFullContext(context.Background(), queryTraceID, "00f067aa0ba902b7", "")
	if _, err := a.DB.Exec(ctx, "SELECT sleep_ms(150), ?", queryArgValue); err != nil {
		t.Fatalf("slow query: %v", err)
	}
	if _, err := a.DB.Exec(ctx, "SELECT sleep_ms(50), ?", queryArgValue); err != nil {
		t.Fatalf("fast query: %v", err)
	}
	m, ok := a.DB.(*orm.Manager)
	if !ok {
		t.Fatalf("DB = %T, want *orm.Manager", a.DB)
	}
	if err := m.FlushQueryEvents(context.Background()); err != nil {
		t.Fatalf("FlushQueryEvents: %v", err)
	}

	content := readLogFile(t, dir)
	slow := linesWith(content, "velocity/orm: slow query")
	if len(slow) != 1 {
		t.Fatalf("slow query lines = %d, want 1:\n%s", len(slow), content)
	}
	line := slow[0]
	for _, want := range []string{"] WARN: velocity/orm: slow query", "SELECT sleep_ms(150), ?", "duration_ms", "trace_id=" + queryTraceID} {
		if !strings.Contains(line, want) {
			t.Errorf("slow query line lacks %q: %s", want, line)
		}
	}
	if strings.Contains(content, queryArgValue) {
		t.Errorf("log file carries the bound value:\n%s", content)
	}
	if n := len(linesWith(content, "sleep_ms(50)")); n != 0 {
		t.Errorf("the 50ms query wrote %d lines, want none:\n%s", n, content)
	}

	marks := map[string]bool{}
	for _, ev := range fake.GetDispatchedEvents() {
		if q, ok := ev.(*orm.QueryExecuted); ok {
			marks[q.SQL] = q.Slow
		}
	}
	for query, want := range map[string]bool{"SELECT sleep_ms(150), ?": true, "SELECT sleep_ms(50), ?": false} {
		got, ok := marks[query]
		if !ok {
			t.Errorf("no QueryExecuted for %q", query)
			continue
		}
		if got != want {
			t.Errorf("QueryExecuted{%q}.Slow = %v, want %v", query, got, want)
		}
	}
}

// With DB_LOG_QUERIES=true and LOG_DRIVER=file, statements land in the log
// file (a transaction's too) and nothing reaches stdout or stderr.
func TestDBLogQueries_FileDriverTakesEveryStatementAndStdoutNone(t *testing.T) {
	t.Setenv("DB_LOG_QUERIES", "true")
	t.Setenv("DB_SLOW_QUERY_THRESHOLD", "0")
	dir := t.TempDir()
	streams := captureStdStreams(t)
	a := newQueryLoggingApp(t, dir)

	ctx := context.Background()
	if _, err := a.DB.Exec(ctx, "CREATE TABLE secrets (value TEXT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := a.DB.Transaction(ctx, func(ctx context.Context) error {
		_, err := a.DB.Exec(ctx, "INSERT INTO secrets (value) VALUES (?)", queryArgValue)
		return err
	}); err != nil {
		t.Fatalf("transaction: %v", err)
	}
	written := streams()

	content := readLogFile(t, dir)
	for _, statement := range []string{"CREATE TABLE secrets (value TEXT)", "INSERT INTO secrets (value) VALUES (?)"} {
		lines := linesWith(content, statement)
		if len(lines) != 1 || !strings.Contains(lines[0], "] DEBUG: velocity/orm: query executed") {
			t.Errorf("log file lines for %q = %q, want one debug query line:\n%s", statement, lines, content)
		}
		if strings.Contains(written, statement) {
			t.Errorf("%q reached a standard stream:\n%s", statement, written)
		}
	}
	if strings.Contains(content, queryArgValue) || strings.Contains(written, queryArgValue) {
		t.Errorf("the bound value was logged:\nfile:\n%s\nstreams:\n%s", content, written)
	}
}
