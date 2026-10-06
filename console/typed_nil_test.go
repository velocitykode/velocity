package console

import (
	"testing"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/scheduler"
)

// The commands treat a typed-nil service as not configured: they warn and
// return nil, as for nil, and call nothing on it.
func TestCommands_TypedNilServiceIsNotConfigured(t *testing.T) {
	var db *orm.Manager
	var cm *cache.Manager
	var sched *scheduler.Scheduler
	for name, run := range map[string]func() error{
		"Migrate":         func() error { return Migrate(db) },
		"MigrateFresh":    func() error { return MigrateFresh(db) },
		"MigrateRollback": func() error { return MigrateRollback(db, 1) },
		"MigrateStatus":   func() error { return MigrateStatus(db) },
		"DBWipe":          func() error { return DBWipe(db) },
		"CacheClear":      func() error { return CacheClear(cm) },
		"ScheduleWork":    func() error { return ScheduleWork(sched) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err != nil {
				t.Errorf("%s(typed nil) = %v, want nil", name, err)
			}
		})
	}
}
