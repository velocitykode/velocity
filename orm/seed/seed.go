// Package seed runs database seeders: small units of code that insert
// reference data (roles, regions, default settings) or development fixtures
// through the ORM and the orm/factory package.
//
// A seeder is any type implementing [Seeder]. Seeders are plain values with
// no registry of their own: an application lists them, in dependency order,
// in the Seeders step of the bootstrap chain and the `vel db seed` command
// runs that list. Composition is ordinary Go: a seeder that needs to run
// others calls [Run] with them.
//
//	type RoleSeeder struct{}
//
//	func (RoleSeeder) Name() string { return "role" }
//
//	func (RoleSeeder) Run(ctx context.Context, db *orm.Manager) error {
//	    for _, name := range []string{"owner", "admin", "member"} {
//	        if _, err := (orm.Model[Role]{}).FirstOrCreate(ctx,
//	            map[string]any{"name": name}, nil); err != nil {
//	            return err
//	        }
//	    }
//	    return nil
//	}
//
//	// database/seeders/kernel.go
//	func Register(r *velocity.Seeders) {
//	    r.Add(&RoleSeeder{}, &UserSeeder{}) // runs in this order
//	}
//
// Every call is context-first. A cancelled context stops the run between
// seeders, and a context carrying a transaction (see orm.TxFromContext) is
// honoured by the ORM and factory writes inside each seeder, which is what
// the transaction-rollback test isolation relies on.
package seed

import (
	"context"

	"github.com/velocitykode/velocity/orm"
)

// Seeder is one unit of seed data.
type Seeder interface {
	// Name identifies the seeder to operators: it is what
	// `vel db seed --only <name>` matches and what progress output prints.
	// Names are kebab-case by convention ("role", "user-profile") and must
	// be unique within an application.
	Name() string

	// Run inserts the seed data. db is the application's ORM manager, for
	// factories and raw access; orm.Model[T] calls need only ctx.
	Run(ctx context.Context, db *orm.Manager) error
}

// Run executes seeders in the order given against db and stops at the first
// failure. It is the composition primitive: a seeder that depends on others
// calls Run with them from inside its own Run.
func Run(ctx context.Context, db *orm.Manager, seeders ...Seeder) error {
	runner, err := NewRunner(db)
	if err != nil {
		return err
	}
	return runner.Run(ctx, seeders...)
}
