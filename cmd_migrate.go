package velocity

import (
	"os"

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

type routesCmd struct{}

func (routesCmd) name() string        { return "routes" }
func (routesCmd) description() string { return "List all registered routes" }
func (routesCmd) run(a *App, args []string) error {
	asJSON := false
	for _, arg := range args {
		if arg == "--json" {
			asJSON = true
			continue
		}
		return unknownToken(arg, arg)
	}
	if asJSON {
		// Bootstrap logs through the console logger and prism, both of
		// which write to os.Stdout; route them to stderr for the duration
		// so stdout carries only the JSON document (vel routes --json | jq).
		realStdout := os.Stdout
		os.Stdout = os.Stderr
		err := a.Bootstrap()
		os.Stdout = realStdout
		if err != nil {
			return err
		}
		return console.RouteListJSON(a.Router, realStdout)
	}
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.RouteList(a.Router)
}

type migrateCmd struct{}

func (migrateCmd) name() string        { return "migrate" }
func (migrateCmd) description() string { return "Run database migrations" }
func (migrateCmd) run(a *App, args []string) error {
	// Parse before Bootstrap so a typo fails fast without starting modules.
	opts, err := parseMigrateArgs(args)
	if err != nil {
		return err
	}
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.Migrate(a.ormDB(), opts)
}

type migrateFreshCmd struct{}

func (migrateFreshCmd) name() string        { return "migrate fresh" }
func (migrateFreshCmd) description() string { return "Drop all tables and re-run migrations" }
func (migrateFreshCmd) run(a *App, args []string) error {
	// Only --force / -f is legal; reject any other token before the guard.
	if err := parseForceOnlyArgs(args); err != nil {
		return err
	}
	if err := guardProductionDataLoss(a, "migrate fresh", args); err != nil {
		return err
	}
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.MigrateFresh(a.ormDB())
}

type migrateRollbackCmd struct{}

func (migrateRollbackCmd) name() string        { return "migrate rollback" }
func (migrateRollbackCmd) description() string { return "Rollback the last migration batch" }
func (migrateRollbackCmd) run(a *App, args []string) error {
	// Parse before the guard/Bootstrap so a typo fails fast. --step, -s and
	// --step=N compose with --force in any order; --force is consumed by the
	// guard below.
	steps, err := parseRollbackArgs(args)
	if err != nil {
		return err
	}
	if err := guardProductionDataLoss(a, "migrate rollback", args); err != nil {
		return err
	}
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.MigrateRollback(a.ormDB(), steps)
}

type migrateStatusCmd struct{}

func (migrateStatusCmd) name() string        { return "migrate status" }
func (migrateStatusCmd) description() string { return "Show migration status" }
func (migrateStatusCmd) run(a *App, args []string) error {
	if err := rejectNoArgs(args); err != nil {
		return err
	}
	if err := a.Bootstrap(); err != nil {
		return err
	}
	return console.MigrateStatus(a.ormDB())
}
