package migrate

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TestMigrator_SqliteLock_StealsStaleLock pins the crash-recovery
// invariant: a lock row left at locked = 1 by a holder that crashed before
// releasing (so locked_at is far in the past) must be stolen by the next
// acquirer rather than bricking every future migration.
func TestMigrator_SqliteLock_StealsStaleLock(t *testing.T) {
	db := openSQLiteForLockTest(t)
	m := NewMigrator(db, "sqlite")
	if err := m.ensureLockTable(); err != nil {
		t.Fatalf("ensureLockTable: %v", err)
	}
	if err := m.seedLockRow(); err != nil {
		t.Fatalf("seedLockRow: %v", err)
	}

	// Simulate a crashed holder: locked, stamped well past the stale cutoff.
	stale := time.Now().Add(-2 * sqliteLockStaleAfter).Unix()
	if _, err := db.Exec(
		"UPDATE migrations_lock SET locked = 1, locked_at = ? WHERE id = 1", stale,
	); err != nil {
		t.Fatalf("stage stale lock: %v", err)
	}

	before := time.Now().Unix()
	if err := m.sqliteAcquireLock(); err != nil {
		t.Fatalf("acquire did not steal stale lock: %v", err)
	}

	var locked int
	var lockedAt int64
	if err := db.QueryRow(
		"SELECT locked, locked_at FROM migrations_lock WHERE id = 1",
	).Scan(&locked, &lockedAt); err != nil {
		t.Fatalf("read lock row: %v", err)
	}
	if locked != 1 {
		t.Fatalf("lock not held after steal (locked=%d)", locked)
	}
	if lockedAt < before {
		t.Fatalf("locked_at not refreshed on steal (got %d, before=%d)", lockedAt, before)
	}
}

// TestMigrator_SqliteLock_FreshLockNotStolen is the inverse: a lock held
// with a fresh locked_at must NOT be stolen; the acquirer keeps spinning.
func TestMigrator_SqliteLock_FreshLockNotStolen(t *testing.T) {
	db := openSQLiteForLockTest(t)
	m := NewMigrator(db, "sqlite")
	if err := m.ensureLockTable(); err != nil {
		t.Fatalf("ensureLockTable: %v", err)
	}
	if err := m.seedLockRow(); err != nil {
		t.Fatalf("seedLockRow: %v", err)
	}

	// A live holder: locked just now.
	if _, err := db.Exec(
		"UPDATE migrations_lock SET locked = 1, locked_at = ? WHERE id = 1", time.Now().Unix(),
	); err != nil {
		t.Fatalf("stage fresh lock: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- m.sqliteAcquireLock() }()

	select {
	case err := <-done:
		t.Fatalf("acquire returned (err=%v) but a fresh lock should block", err)
	case <-time.After(300 * time.Millisecond):
		// Good: still spinning against the live holder.
	}

	// Release so the spinning goroutine can finish (and not leak).
	if _, err := db.Exec(
		"UPDATE migrations_lock SET locked = 0, locked_at = 0 WHERE id = 1",
	); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("acquire after release returned error: %v", err)
	}
}

// TestMigrator_SqliteLock_BackfillsLockedAt covers the real upgrade path: a
// migrations_lock table created before crash-recovery support (no locked_at
// column) that is stuck at locked = 1 from a crashed old runner. ensureLockTable
// must ADD the column (default 0), and that epoch-0 stamp makes the stuck lock
// immediately reclaimable.
func TestMigrator_SqliteLock_BackfillsLockedAt(t *testing.T) {
	db := openSQLiteForLockTest(t)

	// Old-shape table: only (id, locked), and stuck held.
	if _, err := db.Exec(
		"CREATE TABLE migrations_lock (id INTEGER PRIMARY KEY, locked INTEGER NOT NULL DEFAULT 0)",
	); err != nil {
		t.Fatalf("create legacy lock table: %v", err)
	}
	if _, err := db.Exec("INSERT INTO migrations_lock (id, locked) VALUES (1, 1)"); err != nil {
		t.Fatalf("seed stuck lock: %v", err)
	}

	m := NewMigrator(db, "sqlite")
	if err := m.ensureLockTable(); err != nil {
		t.Fatalf("ensureLockTable (should backfill locked_at): %v", err)
	}

	// Column must now exist and the legacy row defaulted to 0.
	var lockedAt int64
	if err := db.QueryRow("SELECT locked_at FROM migrations_lock WHERE id = 1").Scan(&lockedAt); err != nil {
		t.Fatalf("locked_at column missing after backfill: %v", err)
	}
	if lockedAt != 0 {
		t.Fatalf("backfilled locked_at = %d; want 0 (epoch, immediately reclaimable)", lockedAt)
	}

	// The stuck pre-upgrade lock must be reclaimable now.
	if err := m.sqliteAcquireLock(); err != nil {
		t.Fatalf("could not reclaim stuck pre-upgrade lock after backfill: %v", err)
	}
}
