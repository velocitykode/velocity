package migrate

import (
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/orm/drivers"
)

// lockMuProbe is a statement observer that notes whether the migrator's
// lockMu is held when it runs, then runs its hostile code.
type lockMuProbe struct {
	m         atomic.Pointer[Migrator]
	code      *hostile.Code
	calls     atomic.Int64
	underLock atomic.Int64
}

func (p *lockMuProbe) Observing() bool { return true }

func (p *lockMuProbe) ObserveStatement(drivers.StatementEvent) {
	m := p.m.Load()
	if m == nil {
		return
	}
	p.calls.Add(1)
	if m.lockMu.TryLock() {
		m.lockMu.Unlock()
	} else {
		p.underLock.Add(1)
	}
	p.code.Run()
}

// The migration lock's own statements run under lockMu on a holding
// context: an instrumented pool's statement observer runs once lockMu is
// released, so it never sees it held, a panic in it is contained, and a
// call back into the migrator (which takes lockMu) returns.
func TestMigrator_LockStatements_ObserverRunsOffLockMu(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			base := &drivers.BaseDriver{}
			db, err := base.OpenInstrumented("sqlite3", "sqlite", filepath.Join(t.TempDir(), "observed.db"))
			if err != nil {
				t.Fatalf("OpenInstrumented: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			db.SetMaxOpenConns(4)
			m := NewMigrator(db, "sqlite")
			probe := &lockMuProbe{}
			probe.code = hostile.New(t, mode, func() {
				// Re-enter the migrator: withMigrationLock takes lockMu.
				_ = m.withMigrationLock(func() error { return nil })
			})
			if mode == hostile.Block {
				probe.code.Release() // blocks briefly: released up front
			}
			base.SetStatementObserver(probe)
			probe.m.Store(m)
			var err2 error
			if p := hostile.Within(t, hostile.Deadline, func() {
				err2 = m.withMigrationLock(func() error { return nil })
			}); p != nil {
				t.Fatalf("withMigrationLock panicked: %v", p)
			}
			if err2 != nil {
				t.Fatalf("withMigrationLock: %v", err2)
			}
			if probe.calls.Load() == 0 {
				t.Fatal("the observer saw no lock statement")
			}
			if n := probe.underLock.Load(); n != 0 {
				t.Errorf("the observer ran %d time(s) with lockMu held", n)
			}
		})
	}
}
