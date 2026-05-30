// Package sqlite is the opt-in cgo leaf that backs Velocity's ORM with the
// mattn/go-sqlite3 (cgo) SQLite driver. Blank-importing it (directly or via
// orm/standard) overrides the always-on pure-Go modernc default registered by
// the orm root, swapping in the cgo backend under the same "sqlite" /
// "sqlite3" driver names:
//
//	import _ "github.com/velocitykode/velocity/orm/sqlite"
//
// The cgo connector and its registration live in a file gated by the cgo
// build constraint; this doc.go carries no build tag so the package always
// has a compilable file. Under CGO_ENABLED=0 the package is therefore empty
// and importing it is a no-op, leaving the pure-Go modernc default in effect.
package sqlite
