//go:build cgo

package sqlite

import (
	"context"

	_ "github.com/mattn/go-sqlite3"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/drivers"
	"github.com/velocitykode/velocity/orm/internal/sqlitebackend"
)

// init swaps the orm root's pure-Go modernc SQLite default for the cgo mattn
// backend. The root Registers "sqlite"/"sqlite3" with modernc; this leaf
// Overrides those names so a single blank import opts the whole app into cgo
// SQLite without changing any call site. Override (not Register) is required
// because the names are already registered by the root.
func init() {
	factory := func(_ context.Context, cfg drivers.ConnectionConfig) (drivers.Driver, error) {
		return New(cfg)
	}
	orm.Drivers().Override("sqlite", factory)
	orm.Drivers().Override("sqlite3", factory)
}

// New constructs a connected cgo SQLite driver (mattn/go-sqlite3, database/sql
// driver name "sqlite3") from cfg for standalone use without going through the
// ORM driver registry.
func New(cfg drivers.ConnectionConfig) (drivers.Driver, error) {
	d := sqlitebackend.New("sqlite3").(drivers.Driver)
	if err := d.Connect(cfg); err != nil {
		return nil, err
	}
	return d, nil
}
