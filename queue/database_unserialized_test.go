package queue

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Stub database/sql driver
//
// The stub exists to prove locking POLICY, not SQL semantics: it lets the
// tests observe how many DatabaseDriver transactions are open concurrently
// (and block inside BeginTx at will) without a live postgres/mysql server.
// Queries always return zero rows ("no job available"); execs report one
// affected row. Wired via sql.OpenDB so no global sql.Register is needed.
// ---------------------------------------------------------------------------

type stubDialectDriver struct {
	beginHook func() // invoked inside every BeginTx, before the tx is returned
	endHook   func() // invoked on every tx Commit/Rollback
	execHook  func(query string)
}

func (d *stubDialectDriver) Open(string) (driver.Conn, error) { return &stubConn{drv: d}, nil }

type stubDialectConnector struct{ drv *stubDialectDriver }

func (c *stubDialectConnector) Connect(context.Context) (driver.Conn, error) {
	return &stubConn{drv: c.drv}, nil
}
func (c *stubDialectConnector) Driver() driver.Driver { return c.drv }

type stubConn struct{ drv *stubDialectDriver }

func (c *stubConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("stub: Prepare not supported")
}
func (c *stubConn) Close() error { return nil }
func (c *stubConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

// BeginTx implements driver.ConnBeginTx so the sqlite pop path's
// Serializable isolation request is accepted (database/sql rejects
// non-default isolation on drivers without ConnBeginTx).
func (c *stubConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.drv.beginHook != nil {
		c.drv.beginHook()
	}
	return &stubTx{drv: c.drv}, nil
}

func (c *stubConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return emptyRows{}, nil
}

func (c *stubConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if c.drv.execHook != nil {
		c.drv.execHook(query)
	}
	return stubResult{}, nil
}

type stubTx struct{ drv *stubDialectDriver }

func (t *stubTx) Commit() error {
	if t.drv.endHook != nil {
		t.drv.endHook()
	}
	return nil
}
func (t *stubTx) Rollback() error {
	if t.drv.endHook != nil {
		t.drv.endHook()
	}
	return nil
}

type emptyRows struct{}

func (emptyRows) Columns() []string              { return []string{} }
func (emptyRows) Close() error                   { return nil }
func (emptyRows) Next(dest []driver.Value) error { return io.EOF }

type stubResult struct{}

func (stubResult) LastInsertId() (int64, error) { return 1, nil }
func (stubResult) RowsAffected() (int64, error) { return 1, nil }

// TestDatabaseDriver_PopUnserializedOnRowLockDialects proves the pop path
// takes NO driver-wide mutex on dialects with FOR UPDATE SKIP LOCKED: n
// concurrent PopCtxReserved calls must all be inside their transactions
// (blocked in BeginTx) at the same time. With the old process-wide mutex
// only one pop could enter BeginTx at once and the rendezvous would time
// out.
func TestDatabaseDriver_PopUnserializedOnRowLockDialects(t *testing.T) {
	for _, dialect := range []string{"postgres", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			const n = 4
			arrivals := make(chan struct{}, n)
			release := make(chan struct{})
			drv := &stubDialectDriver{
				beginHook: func() {
					arrivals <- struct{}{}
					select {
					case <-release:
					case <-time.After(10 * time.Second):
					}
				},
			}
			db := sql.OpenDB(&stubDialectConnector{drv: drv})
			defer db.Close()
			d := NewDatabaseDriver(db, dialect)

			var wg sync.WaitGroup
			errs := make(chan error, n)
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, _, _, err := d.PopCtxReserved(context.Background(), "parallel")
					errs <- err
				}()
			}

			deadline := time.After(5 * time.Second)
			for got := 0; got < n; got++ {
				select {
				case <-arrivals:
				case <-deadline:
					close(release)
					wg.Wait()
					t.Fatalf("only %d of %d concurrent pops reached BeginTx on %s; pop path appears serialized", got, n, dialect)
				}
			}
			close(release)
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("pop: %v", err)
				}
			}
		})
	}
}

// TestDatabaseDriver_PopStaysSerializedOnSQLite asserts the single-writer
// serialization is preserved on sqlite: across n concurrent pops, at most
// one pop transaction may ever be open at a time. The begin hook widens
// the window so an accidentally unserialized pop path would be observed.
func TestDatabaseDriver_PopStaysSerializedOnSQLite(t *testing.T) {
	const n = 4
	var open, maxOpen atomic.Int32
	drv := &stubDialectDriver{
		beginHook: func() {
			cur := open.Add(1)
			for {
				m := maxOpen.Load()
				if cur <= m || maxOpen.CompareAndSwap(m, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
		},
		endHook: func() { open.Add(-1) },
	}
	db := sql.OpenDB(&stubDialectConnector{drv: drv})
	defer db.Close()
	d := NewDatabaseDriver(db, "sqlite")

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, _, err := d.PopCtxReserved(context.Background(), "serial"); err != nil {
				t.Errorf("pop: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := maxOpen.Load(); got != 1 {
		t.Fatalf("observed %d concurrent pop transactions on sqlite, want exactly 1 (single-writer serialization lost)", got)
	}
}

// TestDatabaseDriver_ClearExcludesPushIfNotExists asserts the invariant
// SKIP LOCKED does NOT cover survives on row-lock dialects: Clear and
// PushIfNotExistsCtx still mutually exclude via d.mu, so Clear's two
// autocommit deletes (jobs, then job_dedupe) can never straddle a
// concurrent claim+insert transaction and strand a dedupe-less jobs row.
func TestDatabaseDriver_ClearExcludesPushIfNotExists(t *testing.T) {
	var clearDeletes atomic.Int32
	arrived := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	drv := &stubDialectDriver{
		beginHook: func() {
			// Only PushIfNotExistsCtx begins a tx in this test; park it
			// inside its critical section while holding d.mu.
			once.Do(func() { close(arrived) })
			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}
		},
		execHook: func(query string) {
			if strings.HasPrefix(query, "DELETE") {
				clearDeletes.Add(1)
			}
		},
	}
	db := sql.OpenDB(&stubDialectConnector{drv: drv})
	defer db.Close()
	d := NewDatabaseDriver(db, "postgres")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.PushIfNotExistsCtx(context.Background(), &TestJob{ID: "dedupe-1"}, "key-1", "q"); err != nil {
			t.Errorf("push: %v", err)
		}
	}()
	<-arrived // push holds d.mu and is parked inside its transaction

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.Clear("q"); err != nil {
			t.Errorf("clear: %v", err)
		}
	}()

	// While the push transaction is parked, Clear must not have issued a
	// single DELETE: it is blocked on d.mu behind the claim+insert.
	time.Sleep(100 * time.Millisecond)
	if got := clearDeletes.Load(); got != 0 {
		t.Fatalf("Clear issued %d DELETE(s) while PushIfNotExistsCtx held the dedupe lock; invariant lost", got)
	}

	close(release)
	wg.Wait()
	if got := clearDeletes.Load(); got != 2 {
		t.Fatalf("Clear issued %d DELETE(s) after release, want 2 (jobs + job_dedupe)", got)
	}
}

// TestDatabaseDriver_ParallelPopPostgres proves, against a real PostgreSQL
// server, that n workers pop in parallel: each goroutine pops a poison row
// and parks inside its own transaction (between the quarantine writes and
// Commit, via the pop-quarantine commit hook) while holding that row's
// FOR UPDATE lock. All n must reach the hook concurrently, which is
// impossible under the old driver-wide mutex, where the first pop would
// hold the lock across its entire transaction and starve the rest.
//
// Requires TEST_POSTGRES_QUEUE=1 and a reachable PostgreSQL instance;
// skipped otherwise (same convention as TestIntegrationDatabaseDriver).
func TestDatabaseDriver_ParallelPopPostgres(t *testing.T) {
	if os.Getenv("TEST_POSTGRES_QUEUE") != "1" {
		t.Skip("set TEST_POSTGRES_QUEUE=1 to run PostgreSQL queue integration tests")
	}

	dbHost := os.Getenv("DB_HOST")
	if dbHost == "" {
		dbHost = "localhost"
	}
	dbUser := os.Getenv("DB_USERNAME")
	if dbUser == "" {
		dbUser = "postgres"
	}
	dbName := os.Getenv("DB_DATABASE")
	if dbName == "" {
		dbName = "velocity_test"
	}

	dsn := fmt.Sprintf("host=%s user=%s dbname=%s sslmode=disable", dbHost, dbUser, dbName)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Skipf("cannot open PostgreSQL DSN: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Skipf("cannot reach PostgreSQL at %s: %v", dbHost, err)
	}

	createTables := `
	DROP TABLE IF EXISTS jobs CASCADE;
	DROP TABLE IF EXISTS failed_jobs CASCADE;

	CREATE TABLE jobs (
		id SERIAL PRIMARY KEY,
		queue VARCHAR(255) NOT NULL,
		payload TEXT NOT NULL,
		attempts INTEGER DEFAULT 0,
		scheduled_at TIMESTAMP NOT NULL,
		reserved_at TIMESTAMP,
		reserved_by VARCHAR(255),
		failed_at TIMESTAMP,
		failed_reason TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE failed_jobs (
		id SERIAL PRIMARY KEY,
		queue VARCHAR(255) NOT NULL,
		payload TEXT NOT NULL,
		exception TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := db.Exec(createTables); err != nil {
		t.Fatalf("Failed to create tables: %v", err)
	}
	defer func() {
		db.Exec("DELETE FROM jobs")
		db.Exec("DELETE FROM failed_jobs")
	}()

	const n = 4
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		// Deliberately malformed payloads: hydration fails, so each pop
		// routes through quarantineAndReturn and parks at the commit hook
		// while its transaction still holds the row's FOR UPDATE lock.
		if _, err := db.Exec(
			`INSERT INTO jobs (queue, payload, attempts, scheduled_at, created_at, updated_at) VALUES ($1, $2, 0, $3, $4, $5)`,
			"pg-parallel", fmt.Sprintf("not-json-%d{{", i), now, now, now,
		); err != nil {
			t.Fatalf("insert poison row %d: %v", i, err)
		}
	}

	arrivals := make(chan struct{}, n)
	release := make(chan struct{})
	restore := setPopQuarantineCommitHookForTest(func() {
		arrivals <- struct{}{}
		select {
		case <-release:
		case <-time.After(15 * time.Second):
		}
	})
	t.Cleanup(restore)

	drv := NewDatabaseDriver(db, "postgres")
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, err := drv.PopCtxReserved(context.Background(), "pg-parallel")
			errs <- err
		}()
	}

	deadline := time.After(10 * time.Second)
	for got := 0; got < n; got++ {
		select {
		case <-arrivals:
		case <-deadline:
			close(release)
			wg.Wait()
			t.Fatalf("only %d of %d workers were inside a pop transaction concurrently; pops appear serialized", got, n)
		}
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrPoisonJob) {
			t.Fatalf("pop: want ErrPoisonJob, got %v", err)
		}
	}

	// All n rows quarantined exactly once each: SKIP LOCKED handed every
	// concurrent transaction a distinct row.
	var jobsLeft, failed int
	if err := db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = 'pg-parallel'").Scan(&jobsLeft); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM failed_jobs WHERE queue = 'pg-parallel'").Scan(&failed); err != nil {
		t.Fatalf("count failed_jobs: %v", err)
	}
	if jobsLeft != 0 || failed != n {
		t.Fatalf("after parallel pops: jobs=%d (want 0), failed_jobs=%d (want %d)", jobsLeft, failed, n)
	}
}
