package velocity

import (
	"strconv"

	"github.com/velocitykode/velocity/console"
	"github.com/velocitykode/velocity/orm"
)

// ormDB returns the database as the richer orm.Database. a.DB is typed as the
// stdlib-only contract.Database (so the app leaf need not import orm); the
// stored value is always the concrete *orm.Manager, which also satisfies
// orm.Database. Returns nil when no database is configured.
func (a *App) ormDB() orm.Database {
	db, _ := a.DB.(orm.Database)
	return db
}

type routeListCmd struct{}

func (routeListCmd) name() string        { return "route:list" }
func (routeListCmd) description() string { return "List all registered routes" }
func (routeListCmd) run(a *App, args []string) error {
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.RouteList(a.Router)
}

type migrateCmd struct{}

func (migrateCmd) name() string        { return "migrate" }
func (migrateCmd) description() string { return "Run database migrations" }
func (migrateCmd) run(a *App, args []string) error {
	if err := a.Bootstrap(); err != nil {
		return err
	}
	opts := console.MigrateOptions{}
	for _, arg := range args {
		if arg == "--pretend" {
			opts.Pretend = true
		}
	}
	return console.Migrate(a.ormDB(), opts)
}

type migrateFreshCmd struct{}

func (migrateFreshCmd) name() string        { return "migrate:fresh" }
func (migrateFreshCmd) description() string { return "Drop all tables and re-run migrations" }
func (migrateFreshCmd) run(a *App, args []string) error {
	if err := guardProductionDataLoss(a, "migrate:fresh", args); err != nil {
		return err
	}
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.MigrateFresh(a.ormDB())
}

type migrateRollbackCmd struct{}

func (migrateRollbackCmd) name() string        { return "migrate:rollback" }
func (migrateRollbackCmd) description() string { return "Rollback the last migration batch" }
func (migrateRollbackCmd) run(a *App, args []string) error {
	if err := guardProductionDataLoss(a, "migrate:rollback", args); err != nil {
		return err
	}
	if err := a.Bootstrap(); err != nil {
		return err
	}
	// Scan rather than require --step at args[0] so it composes with
	// --force in any order.
	steps := 1
	for i := 0; i < len(args); i++ {
		if (args[i] == "--step" || args[i] == "-s") && i+1 < len(args) {
			if n, err := strconv.Atoi(args[i+1]); err == nil {
				steps = n
			}
			i++
		}
	}
	return console.MigrateRollback(a.ormDB(), steps)
}

type migrateStatusCmd struct{}

func (migrateStatusCmd) name() string        { return "migrate:status" }
func (migrateStatusCmd) description() string { return "Show migration status" }
func (migrateStatusCmd) run(a *App, args []string) error {
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.MigrateStatus(a.ormDB())
}
