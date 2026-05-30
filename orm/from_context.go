package orm

import "github.com/velocitykode/velocity/contract"

// dbProvider is anything that exposes a database handle as the narrow
// contract.Database interface - notably *router.Context (via ctx.DB()). We accept
// it structurally so orm never imports router (router sits above orm; importing it
// back would create a cycle).
type dbProvider interface {
	DB() contract.Database
}

// FromContext recovers the full orm.Database API from a context whose DB()
// accessor returns the narrow contract.Database. The concrete value behind
// ctx.DB() is *Manager, which implements the full Database interface, so this is
// a checked type assertion. Mirrors auth.FromContext / csrf.FromContext /
// view.FromContext: handlers get the narrow contract type by default and reach
// for FromContext when they need the rich, package-specific API.
//
// Returns nil if no database is wired or the handle is not an orm Database.
//
//	db := orm.FromContext(ctx) // *orm.Manager behind orm.Database
//	db.AddConnection("read-replica", driver)
func FromContext(p dbProvider) Database {
	if p == nil {
		return nil
	}
	d, _ := p.DB().(Database)
	return d
}
