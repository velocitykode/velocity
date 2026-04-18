package velocity

import (
	"strconv"

	"github.com/velocitykode/velocity/console"
)

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
	return console.Migrate(a.DB, opts)
}

type migrateFreshCmd struct{}

func (migrateFreshCmd) name() string        { return "migrate:fresh" }
func (migrateFreshCmd) description() string { return "Drop all tables and re-run migrations" }
func (migrateFreshCmd) run(a *App, args []string) error {
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.MigrateFresh(a.DB)
}

type migrateRollbackCmd struct{}

func (migrateRollbackCmd) name() string        { return "migrate:rollback" }
func (migrateRollbackCmd) description() string { return "Rollback the last migration batch" }
func (migrateRollbackCmd) run(a *App, args []string) error {
	if err := a.Bootstrap(); err != nil {
		return err
	}
	steps := 1
	if len(args) >= 2 && (args[0] == "--step" || args[0] == "-s") {
		if n, err := strconv.Atoi(args[1]); err == nil {
			steps = n
		}
	}
	return console.MigrateRollback(a.DB, steps)
}

type migrateStatusCmd struct{}

func (migrateStatusCmd) name() string        { return "migrate:status" }
func (migrateStatusCmd) description() string { return "Show migration status" }
func (migrateStatusCmd) run(a *App, args []string) error {
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.MigrateStatus(a.DB)
}
