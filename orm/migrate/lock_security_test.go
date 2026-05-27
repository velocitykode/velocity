package migrate

import (
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// reentrancyMigrationRuns counts executions of the re-entrance test
// migration. Registered in init() so the assertion is stable across
// test re-runs within the same binary.
var reentrancyMigrationRuns atomic.Int32

func init() {
	Register(&Migration{
		Version:     "20260102000001",
		Description: "reentrancy test migration",
		Up: func(mi *Migrator) error {
			reentrancyMigrationRuns.Add(1)
			if err := mi.CreateTable("reentrancy_test", func(b *TableBuilder) {
				b.ID()
				b.String("name")
			}); err != nil {
				return err
			}
			if err := mi.AddColumn("reentrancy_test", "extra", func(c *ColumnBuilder) {
				c.String(64).Nullable()
			}); err != nil {
				return err
			}
			if err := mi.CreateIndex("idx_reentrancy_test_name", "reentrancy_test", func(b *IndexBuilder) {
				b.Columns("name")
			}); err != nil {
				return err
			}
			if err := mi.Index("reentrancy_test", "extra"); err != nil {
				return err
			}
			if err := mi.DropIndex("idx_reentrancy_test_name"); err != nil {
				return err
			}
			return mi.Raw("SELECT 1")
		},
		Down: func(mi *Migrator) error {
			return mi.DropTable("reentrancy_test")
		},
	})
}

// O-05 regression suite. Before the fix, Down() and Fresh() bypassed the
// migration lock that Up() acquired, so two concurrent migrator processes
// could mutate the schema simultaneously. These tests pin the new
// invariant: every schema-mutating entry point (Up, Down, Fresh, plus the
// public DDL helpers) serialises through withMigrationLock, and the lock
// is re-entrant within a single Migrator instance so nested DDL helpers
// called from a migration body never deadlock.

// openSQLiteForLockTest opens a fresh on-disk SQLite database for the
// calling test and registers its cleanup. A file-backed DB (vs. :memory:)
// is required so multiple *sql.DB pool connections see the same schema,
// which is what makes the row-CAS lock observable across goroutines.
func openSQLiteForLockTest(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "lock_security.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(4)
	return db
}

// holdLock blocks the test goroutine until it has acquired the migration
// lock on the supplied Migrator, then returns a release function. Used to
// stage the "host A holds the lock" precondition for the concurrent
// tests below without depending on goroutine scheduling order.
func holdLock(t *testing.T, m *Migrator) func() {
	t.Helper()
	release, err := m.acquireMigrationLock()
	if err != nil {
		t.Fatalf("acquireMigrationLock: %v", err)
	}
	return release
}

// TestMigrator_Down_SerialisesAgainstUp pins the invariant from finding
// O-05: when one migrator holds the lock (simulating host A running Up),
// a second migrator's Down() call must block until the lock is released.
// Before the fix Down ran unlocked and could mutate the schema
// concurrently with Up, corrupting it.
func TestMigrator_Down_SerialisesAgainstUp(t *testing.T) {
	db := openSQLiteForLockTest(t)
	holder := NewMigrator(db, "sqlite")

	// Host A: take the migration lock and hold it.
	release := holdLock(t, holder)

	// Host B: try to Down() concurrently. Must NOT proceed while A holds
	// the lock; the goroutine should still be blocked when we check it
	// after a short grace period.
	contender := NewMigrator(db, "sqlite")
	done := make(chan error, 1)
	go func() {
		done <- contender.Down(1)
	}()

	select {
	case err := <-done:
		release()
		t.Fatalf("Down() returned (err=%v) while migration lock was held; expected to block", err)
	case <-time.After(200 * time.Millisecond):
		// Good: still blocked.
	}

	// Releasing the lock should unblock the contender. Use a generous
	// timeout because sqliteAcquireLock spins with 50ms backoff.
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Down() after release returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Down() did not return within 5s after lock release")
	}
}

// TestMigrator_Fresh_SerialisesAgainstUp pins the invariant for Fresh()
// specifically: it must hold the lock across BOTH its drop pass and the
// Up() that follows, so a concurrent migrator cannot apply migrations to
// tables that Fresh is in the middle of dropping. The test stages a held
// lock and asserts Fresh blocks.
func TestMigrator_Fresh_SerialisesAgainstUp(t *testing.T) {
	db := openSQLiteForLockTest(t)
	holder := NewMigrator(db, "sqlite")

	release := holdLock(t, holder)

	contender := NewMigrator(db, "sqlite")
	done := make(chan error, 1)
	go func() {
		done <- contender.Fresh()
	}()

	select {
	case err := <-done:
		release()
		t.Fatalf("Fresh() returned (err=%v) while migration lock was held; expected to block", err)
	case <-time.After(200 * time.Millisecond):
		// Good: still blocked.
	}

	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Fresh() after release returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Fresh() did not return within 5s after lock release")
	}
}

// TestMigrator_Fresh_HoldsLockAcrossDropAndUp asserts the
// "single-acquire-spanning-drop+up" requirement from finding O-05.
// Inside Fresh's body the nested Up() call must NOT release the lock
// between phases. We pin this directly by hooking into the test's own
// observation point: an outer caller takes the migration lock via
// withMigrationLock, then calls Fresh-like sub-operations and verifies
// that lockDepth never returns to 0 across the sequence.
//
// The test runs Fresh on one migrator while a second migrator races to
// acquire the same driver lock. Because the second migrator's acquire
// would land inside Fresh's drop-to-Up window if Fresh released early,
// we count how many distinct "lock holders" Fresh saw by inspecting
// whether m2's acquire happened before m1.Fresh returned.
func TestMigrator_Fresh_HoldsLockAcrossDropAndUp(t *testing.T) {
	db := openSQLiteForLockTest(t)

	m1 := NewMigrator(db, "sqlite")
	m2 := NewMigrator(db, "sqlite")

	var (
		m1FreshReturned atomic.Bool
		m2AcquiredEarly atomic.Bool
	)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := m1.Fresh(); err != nil {
			t.Errorf("Fresh() error: %v", err)
		}
		m1FreshReturned.Store(true)
	}()
	go func() {
		defer wg.Done()
		// Stagger so Fresh has a chance to grab the lock first.
		time.Sleep(10 * time.Millisecond)
		if err := m2.withMigrationLock(func() error {
			// Record whether we got into the critical section before
			// Fresh returned. If Fresh held the lock across both
			// phases (drop and Up) as required by O-05, we must have
			// been blocked until after m1FreshReturned was set.
			if !m1FreshReturned.Load() {
				m2AcquiredEarly.Store(true)
			}
			return nil
		}); err != nil {
			t.Errorf("m2.withMigrationLock: %v", err)
		}
	}()
	wg.Wait()

	if m2AcquiredEarly.Load() {
		t.Fatal("second migrator acquired the lock before Fresh() returned; " +
			"Fresh did not hold the lock across the drop-then-Up pipeline (O-05)")
	}
}

// TestMigrator_DDLHelper_ReentrantFromMigrationBody verifies the
// re-entrance contract for DDL helpers. A migration body that invokes
// public DDL helpers (CreateTable, CreateIndex, Raw, ...) must not
// deadlock against the migration lock that Up() already holds; the
// inner withMigrationLock call must observe lockDepth > 0 and skip
// re-acquiring.
//
// The migration itself is registered in init() above; this test just
// drives Up and checks the invariants.
func TestMigrator_DDLHelper_ReentrantFromMigrationBody(t *testing.T) {
	db := openSQLiteForLockTest(t)
	m := NewMigrator(db, "sqlite")

	reentrancyMigrationRuns.Store(0)

	// Run with a hard timeout: if re-entrance is broken any of the
	// inner DDL calls will block forever on the driver primitive
	// because the outer Up() already holds it.
	done := make(chan error, 1)
	go func() {
		done <- m.Up()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Up() with nested DDL helpers returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Up() with nested DDL helpers deadlocked; re-entrance is broken")
	}

	if got := reentrancyMigrationRuns.Load(); got != 1 {
		t.Fatalf("re-entrance migration body ran %d times; want 1", got)
	}

	// Final invariant: lock fully released after Up returns. If the
	// re-entrance counter underflowed or leaked a held lock, depth
	// would be non-zero here.
	m.lockMu.Lock()
	depth := m.lockDepth
	rel := m.lockRelease
	m.lockMu.Unlock()
	if depth != 0 {
		t.Fatalf("lockDepth = %d after Up() returned; expected 0", depth)
	}
	if rel != nil {
		t.Fatalf("lockRelease still set after Up() returned; expected nil")
	}
}

// TestMigrator_TwoMigratorsContendForLock covers the original audit
// scenario directly: two independent Migrator instances against the
// same DB enter withMigrationLock concurrently. Exactly one proceeds
// at a time; both succeed in the end. This is the lower-level cousin
// of the Down/Fresh-versus-Up tests above and pins the underlying
// driver-level serialisation regardless of which entry point each
// caller used.
func TestMigrator_TwoMigratorsContendForLock(t *testing.T) {
	db := openSQLiteForLockTest(t)

	// Track concurrent critical-section execution: if locking is
	// broken, both goroutines will be inside withMigrationLock at the
	// same time and inFlight will exceed 1.
	var inFlight, maxInFlight atomic.Int32

	// Two distinct Migrator instances against the same DB, so the
	// re-entrance counter cannot mask a missing lock. The only thing
	// that can serialise them is the driver-level lock.
	m1 := NewMigrator(db, "sqlite")
	m2 := NewMigrator(db, "sqlite")

	run := func() error {
		v := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			cur := maxInFlight.Load()
			if v <= cur || maxInFlight.CompareAndSwap(cur, v) {
				break
			}
		}
		// Hold the lock for a noticeable window so the other goroutine
		// has a real chance to race in if locking is broken.
		time.Sleep(50 * time.Millisecond)
		return nil
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)

	wg.Add(1)
	go func() {
		defer wg.Done()
		errs[0] = m1.withMigrationLock(run)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		errs[1] = m2.withMigrationLock(run)
	}()
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d returned error: %v", i, err)
		}
	}
	if got := maxInFlight.Load(); got > 1 {
		t.Fatalf("two migrators ran concurrently inside withMigrationLock (max in-flight = %d); lock is broken", got)
	}
}
