package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// newSQLiteQueueDB spins up a per-test SQLite database with the jobs and
// failed_jobs tables the DatabaseDriver expects. It returns the driver
// pointed at that DB plus a cleanup func.
//
// File-backed (t.TempDir()) rather than in-memory: a context-cancelled
// transaction inside PopCtxWithTrace causes database/sql to discard the
// connection on which the tx was running, and SQLite in-memory databases
// live on that single connection. After the cancel, follow-up queries open
// a fresh connection and see an empty DB ("no such table: jobs"), which
// flaked TestC01_DatabaseDriver_PoisonRowSurvivesCallerCancellation under
// -race. File-backed schema survives reconnects. Each call gets a unique
// path under t.TempDir() so parallel subtests cannot collide.
func newSQLiteQueueDB(t *testing.T) (*DatabaseDriver, func()) {
	t.Helper()

	dsn := "file:" + t.TempDir() + "/queue.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Cap the pool at 1 connection. The DatabaseDriver pop path holds d.mu
	// across BeginTx; with multiple conns SQLite can throw "database is
	// locked" on WAL writers. A single connection serialises access at the
	// sql.DB layer instead of relying on SQLite's lock manager. Scaling the
	// pool is a DB-level concern, not the pop-correctness concern these
	// tests are validating.
	db.SetMaxOpenConns(1)

	schema := []string{
		`CREATE TABLE jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			queue TEXT NOT NULL,
			payload TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			scheduled_at DATETIME NOT NULL,
			reserved_at DATETIME,
			reserved_by TEXT,
			failed_at DATETIME,
			failed_reason TEXT,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE failed_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			queue TEXT NOT NULL,
			payload TEXT NOT NULL,
			exception TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
	}
	for _, s := range schema {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}

	driver := NewDatabaseDriver(db, "sqlite")
	// Register TestJob so GetJobFromWrapper can restore it.
	Register("*queue.TestJob", func(data []byte) (Job, error) {
		j := &TestJob{}
		if err := json.Unmarshal(data, j); err != nil {
			return nil, err
		}
		return j, nil
	})

	return driver, func() { _ = db.Close() }
}

// TestDatabaseDriver_Pop_TwoWorkers_SingleJob asserts that a single job
// inserted into the DB is delivered to exactly one worker even when N
// workers race to Pop it concurrently. This is the core correctness
// invariant of the transactional Pop implementation.
func TestDatabaseDriver_Pop_TwoWorkers_SingleJob(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil) // disable signing for simplicity

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	// Push exactly one job.
	job := &TestJob{ID: "only-one", Message: "race-me"}
	if err := driver.PushCtx(context.Background(), job, "pop-race"); err != nil {
		t.Fatalf("push: %v", err)
	}

	const workers = 16
	var popped int32
	var wg sync.WaitGroup

	// Gate all goroutines to start at the same instant to maximise contention.
	start := make(chan struct{})

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Each worker tries up to N times to Pop; at most one will succeed.
			for attempt := 0; attempt < 5; attempt++ {
				got, err := driver.PopCtx(context.Background(), "pop-race")
				if err != nil {
					t.Errorf("pop error: %v", err)
					return
				}
				if got != nil {
					atomic.AddInt32(&popped, 1)
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}

	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&popped); got != 1 {
		t.Fatalf("expected exactly one worker to pop the job, got %d", got)
	}
	size, err := driver.Size("pop-race")
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	if size != 0 {
		t.Errorf("expected queue empty after race, got size=%d", size)
	}
}

// TestDatabaseDriver_Pop_RejectsTamperedPayload verifies that a row whose
// HMAC signature does not validate is treated as poison and routed through
// the quarantine path: it is removed from `jobs` and recorded in
// `failed_jobs` with the integrity-failure exception. The earlier
// "preserved for forensic inspection" property is honoured by the
// failed_jobs row, not by leaving the poison row live in the queue (which
// would head-of-line starve every other due job after C-01-fb4).
func TestDatabaseDriver_Pop_RejectsTamperedPayload(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey([]byte("integrity-test-key"))

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	job := &TestJob{ID: "tamper-me", Message: "please"}
	if err := driver.PushCtx(context.Background(), job, "tamper-queue"); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Tamper with the stored payload: replace the ID so the HMAC no longer matches.
	row := driver.db.QueryRow("SELECT id, payload FROM jobs WHERE queue = ?", "tamper-queue")
	var id int
	var payload string
	if err := row.Scan(&id, &payload); err != nil {
		t.Fatalf("scan: %v", err)
	}
	var wrapper map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &wrapper); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Mutate nested payload Data field to trigger signature mismatch.
	p, ok := wrapper["payload"].(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected wrapper shape: %T", wrapper["payload"])
	}
	// Replace the Data field with arbitrary bytes; the signature is now invalid.
	p["data"] = json.RawMessage(`{"id":"MALICIOUS","message":"evil"}`)
	tampered, err := json.Marshal(wrapper)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := driver.db.Exec("UPDATE jobs SET payload = ? WHERE id = ?", string(tampered), id); err != nil {
		t.Fatalf("update: %v", err)
	}

	popped, popErr := driver.PopCtx(context.Background(), "tamper-queue")
	if popErr == nil {
		t.Fatalf("expected integrity error, got nil (job=%v)", popped)
	}
	if !errors.Is(popErr, ErrPoisonJob) {
		t.Errorf("integrity error did not wrap ErrPoisonJob (worker would not treat as recoverable): %v", popErr)
	}
	if popped != nil {
		t.Errorf("expected nil job on integrity failure, got %T", popped)
	}

	// Tampered row must be GONE from jobs (head-of-line starvation guard).
	var liveCount int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "tamper-queue").Scan(&liveCount); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if liveCount != 0 {
		t.Errorf("tampered row left live in jobs: count=%d (HOL starvation regression)", liveCount)
	}

	// And recorded in failed_jobs with the integrity-failure exception
	// preserved for forensic inspection.
	var failedCount int
	var exception, storedPayload string
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM failed_jobs WHERE queue = ?", "tamper-queue").Scan(&failedCount); err != nil {
		t.Fatalf("count failed_jobs: %v", err)
	}
	if failedCount != 1 {
		t.Fatalf("tampered row not recorded in failed_jobs: count=%d", failedCount)
	}
	if err := driver.db.QueryRow("SELECT exception, payload FROM failed_jobs WHERE queue = ?", "tamper-queue").Scan(&exception, &storedPayload); err != nil {
		t.Fatalf("scan failed_jobs row: %v", err)
	}
	if !strings.Contains(exception, "integrity check failed") {
		t.Errorf("failed_jobs.exception does not name the integrity-failure cause: %q", exception)
	}
	if storedPayload != string(tampered) {
		t.Errorf("failed_jobs.payload does not match the on-wire (tampered) bytes; operator cannot inspect what came off the queue")
	}
}

// TestDatabaseDriver_Push_Size_Clear covers the placeholder rewriting for
// non-Postgres drivers: every code path must survive with `?` placeholders.
func TestDatabaseDriver_Push_Size_Clear(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	for i := 0; i < 3; i++ {
		if err := driver.PushCtx(context.Background(), &TestJob{ID: fmt.Sprintf("job-%d", i)}, "q"); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}
	size, err := driver.Size("q")
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	if size != 3 {
		t.Errorf("size=%d, want 3", size)
	}
	if err := driver.Clear("q"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	size, _ = driver.Size("q")
	if size != 0 {
		t.Errorf("size after clear=%d, want 0", size)
	}
}

// TestDatabaseDriver_RewriteQuery exercises the placeholder rewriter directly
// so regressions are caught even if no driver-specific test runs.
func TestDatabaseDriver_RewriteQuery(t *testing.T) {
	cases := []struct {
		name     string
		dbDriver string
		in       string
		want     string
	}{
		{"postgres untouched", "postgres", "WHERE a = $1 AND b = $2", "WHERE a = $1 AND b = $2"},
		{"sqlite rewrite", "sqlite", "WHERE a = $1 AND b = $2", "WHERE a = ? AND b = ?"},
		{"mysql rewrite", "mysql", "SET x = $10 WHERE y = $1", "SET x = ? WHERE y = ?"},
		{"no placeholders", "sqlite", "SELECT COUNT(*) FROM t", "SELECT COUNT(*) FROM t"},
		{"literal $ untouched", "sqlite", "x = '$foo'", "x = '$foo'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &DatabaseDriver{dbDriver: tc.dbDriver}
			got := d.rewriteQuery(tc.in)
			if got != tc.want {
				t.Errorf("rewriteQuery(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
