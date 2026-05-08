package orm

import (
	"context"
	"fmt"

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

	registerSQL("sqlite", drivers.NewSQLiteDriver)
	registerSQL("sqlite3", drivers.NewSQLiteDriver)
	registerSQL("postgres", drivers.NewPostgresDriver)
	registerSQL("mysql", drivers.NewMySQLDriver)
}
