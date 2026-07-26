package orm

import (
	"context"
	"fmt"

	// modernc.org/sqlite is the pure-Go SQLite driver. It self-registers with
	// database/sql under the name "sqlite" and is the always-on, cgo-free
	// default so zero-config apps and `go test ./...` get a working DB without
	// a blank import. The cgo mattn backend and postgres/mysql are opt-in via
	// their orm/{sqlite,postgres,mysql} leaf packages.
	_ "modernc.org/sqlite"

	"github.com/velocitykode/velocity/driverregistry"
	"github.com/velocitykode/velocity/orm/drivers"
)

// driverRegistry is the canonical Velocity driver registry for ORM
// drivers. Built-in drivers (sqlite, postgres, mysql) self-register from
// this file's init(). Each registered factory constructs a connected
// drivers.Driver from the supplied ConnectionConfig so callers always
// receive a ready-to-query handle.
var driverRegistry = driverregistry.New[drivers.Driver, drivers.ConnectionConfig]("orm")

// Drivers returns the registry that ORM driver factories register
// themselves into. Use this from a driver package's init() to install
// support for an additional database backend:
//
//	func init() {
//	    orm.Drivers().Register("clickhouse", func(_ context.Context, cfg drivers.ConnectionConfig) (drivers.Driver, error) {
//	        d := newClickhouseDriver()
//	        if err := d.Connect(cfg); err != nil {
//	            return nil, err
//	        }
//	        return d, nil
//	    })
//	}
//
// The factory is responsible for both constructing the driver and calling
// Connect; returning a fully connected handle keeps NewManager free of
// driver-specific knowledge.
//
// Query telemetry (query.executed / query.failed) is emitted from the
// database/sql driver wrapper, so a registered driver inherits it only if it
// opens its pool through drivers.BaseDriver.OpenAndPing or
// drivers.OpenInstrumented. A driver that calls sql.Open directly is invisible
// to APM, including for statements the ORM itself issues against it.
func Drivers() *driverregistry.Registry[drivers.Driver, drivers.ConnectionConfig] {
	return driverRegistry
}

func init() {
	registerSQL := func(name string, ctor func() drivers.Driver) {
		Drivers().Register(name, func(_ context.Context, cfg drivers.ConnectionConfig) (drivers.Driver, error) {
			d := ctor()
			if err := d.Connect(cfg); err != nil {
				return nil, fmt.Errorf("velocity/orm: %s connect: %w", name, err)
			}
			return d, nil
		})
	}

	// Only the pure-Go modernc SQLite default is registered in the root, under
	// both "sqlite" and "sqlite3". drivers.NewSQLiteDriver opens the modernc
	// "sqlite" database/sql driver (blank-imported above), so a zero-config app
	// gets a working DB with no cgo and no extra blank import.
	//
	// postgres, mysql, and the cgo mattn SQLite backend are NOT registered
	// here. They live in the orm/postgres, orm/mysql, and orm/sqlite leaf
	// packages (which carry lib/pq, go-sql-driver/mysql, and the cgo
	// dependency respectively) and self-register from their own init(). Blank-
	// import the specific leaf, or orm/standard for the full set.
	registerSQL("sqlite", drivers.NewSQLiteDriver)
	registerSQL("sqlite3", drivers.NewSQLiteDriver)
}
