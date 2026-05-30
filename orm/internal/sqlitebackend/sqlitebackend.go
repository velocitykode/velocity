// Package sqlitebackend is an internal seam between orm/drivers and the cgo
// SQLite leaf (orm/sqlite). It lets the leaf construct a SQLite driver bound
// to the mattn "sqlite3" database/sql backend without orm/drivers exposing a
// backend-selecting constructor in its public API.
//
// orm/drivers installs New from its package init; the leaf reads it. The
// value is typed as any (not drivers.Driver) to keep this package free of an
// import edge back to orm/drivers, which would otherwise form a cycle.
package sqlitebackend

// New builds a SQLite driver bound to the named database/sql driver
// ("sqlite" for the pure-Go modernc default, "sqlite3" for the cgo mattn
// leaf). It is installed by orm/drivers at init time; callers type-assert the
// result to drivers.Driver.
var New func(sqlDriver string) any
