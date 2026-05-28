package queue_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	_ "github.com/mattn/go-sqlite3"

	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/queue/queuetest"
)

// contractShutdownCtx is a short-deadline ctx used to Shutdown the driver
// during t.Cleanup. Bound so a flaky driver shutdown does not stall the
// test process.
func contractShutdownCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestMemoryDriver_Contract runs the queuetest spec against the in-process
// memory driver.
func TestMemoryDriver_Contract(t *testing.T) {
	queuetest.RunDriverContractTests(t, func(t *testing.T) queue.Driver {
		d := queue.NewMemoryDriver()
		d.Start()
		t.Cleanup(func() {
			_ = d.Shutdown(contractShutdownCtx(t))
		})
		return d
	})
}

func TestMemoryDriver_DedupeContract(t *testing.T) {
	queuetest.RunDedupeAwarePusherContract(t, func(t *testing.T) queue.Driver {
		d := queue.NewMemoryDriver()
		d.Start()
		t.Cleanup(func() {
			_ = d.Shutdown(contractShutdownCtx(t))
		})
		return d
	})
}

func TestMemoryDriver_ReservationContract(t *testing.T) {
	queuetest.RunReservationDriverContract(t, func(t *testing.T) queue.Driver {
		d := queue.NewMemoryDriver()
		d.Start()
		t.Cleanup(func() {
			_ = d.Shutdown(contractShutdownCtx(t))
		})
		return d
	})
}

func TestDatabaseDriver_Contract(t *testing.T) {
	queuetest.RunDriverContractTests(t, func(t *testing.T) queue.Driver {
		return newSQLiteContractDriver(t)
	})
}

func TestDatabaseDriver_DedupeContract(t *testing.T) {
	queuetest.RunDedupeAwarePusherContract(t, func(t *testing.T) queue.Driver {
		return newSQLiteContractDriver(t)
	})
}

func TestDatabaseDriver_ReservationContract(t *testing.T) {
	queuetest.RunReservationDriverContract(t, func(t *testing.T) queue.Driver {
		return newSQLiteContractDriver(t)
	})
}

// newSQLiteContractDriver builds a per-test SQLite-backed DatabaseDriver
// with the schema the driver expects. Signing is disabled so the contract
// runner can use a minimal job fixture without configuring a key.
func newSQLiteContractDriver(t *testing.T) queue.Driver {
	t.Helper()

	// Signing is package-global; the contract runner does not exercise
	// integrity verification, so disable it for the duration of the test.
	// Tests that need signing reinstall it explicitly.
	queue.SetSigningKey(nil)

	dsn := "file:" + t.TempDir() + "/queue-contract.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

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
		`CREATE TABLE job_dedupe (
			dedupe_key TEXT PRIMARY KEY,
			queue TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, s := range schema {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}

	return queue.NewDatabaseDriver(db, "sqlite")
}

// TestRedisDriver_Contract runs the queuetest spec against the Redis queue
// driver backed by miniredis.
func TestRedisDriver_Contract(t *testing.T) {
	queuetest.RunDriverContractTests(t, func(t *testing.T) queue.Driver {
		return newMiniredisContractDriver(t)
	})
}

// TestRedisDriver_DedupeContract verifies the Redis driver implements the
// DedupeAwarePusher capability correctly.
func TestRedisDriver_DedupeContract(t *testing.T) {
	queuetest.RunDedupeAwarePusherContract(t, func(t *testing.T) queue.Driver {
		return newMiniredisContractDriver(t)
	})
}

// newMiniredisContractDriver spins a per-test miniredis instance and returns
// a RedisDriver pointed at it. Signing is disabled so the contract runner
// can use a minimal job fixture without configuring a key.
func newMiniredisContractDriver(t *testing.T) queue.Driver {
	t.Helper()

	queue.SetSigningKey(nil)

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	d, err := queue.NewRedisDriver(queue.RedisConfig{
		Host: mr.Host(),
		Port: mr.Port(),
		DB:   "0",
	})
	if err != nil {
		t.Fatalf("NewRedisDriver: %v", err)
	}
	t.Cleanup(func() { _ = d.Shutdown(contractShutdownCtx(t)) })
	return d
}
