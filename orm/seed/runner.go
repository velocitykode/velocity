package seed

import (
	"fmt"

	"github.com/velocitykode/velocity/orm"
)

// Runner executes seeders against a database connection.
type Runner struct {
	manager *orm.Manager
	ran     []string // names of seeders that have been run
}

// NewRunner creates a new Runner instance.
// Returns an error if manager is nil or has no active database connection.
func NewRunner(manager *orm.Manager) (*Runner, error) {
	if manager == nil {
		return nil, fmt.Errorf("seed: manager cannot be nil")
	}
	if manager.DB() == nil {
		return nil, fmt.Errorf("seed: manager has no active database connection")
	}

	return &Runner{
		manager: manager,
		ran:     make([]string, 0),
	}, nil
}

// Run executes a single seeder by name from the global registry.
func (r *Runner) Run(name string) error {
	seeder, err := globalRegistry.Find(name)
	if err != nil {
		return err
	}

	if err := seeder.Run(r.manager); err != nil {
		return fmt.Errorf("seed: seeder %s failed: %w", name, err)
	}

	r.ran = append(r.ran, name)
	return nil
}

// RunAll executes all registered seeders.
// If a "DatabaseSeeder" is registered, only that seeder is run
// (it is responsible for calling other seeders via Call).
// Otherwise, all registered seeders are run in registration order.
func (r *Runner) RunAll() error {
	// "DatabaseSeeder" is the conventional aggregator name — when present
	// it runs alone and is expected to dispatch the others via Call.
	if _, err := globalRegistry.Find("DatabaseSeeder"); err == nil {
		return r.Run("DatabaseSeeder")
	}

	// No DatabaseSeeder -- run all seeders in registration order
	seeders := globalRegistry.All()
	for _, seeder := range seeders {
		if err := seeder.Run(r.manager); err != nil {
			return fmt.Errorf("seed: seeder %s failed: %w", seeder.Name(), err)
		}
		r.ran = append(r.ran, seeder.Name())
	}
	return nil
}

// Call executes one or more seeders by name. This is the method
// a DatabaseSeeder uses inside its Run() to invoke other seeders.
//
// Usage inside a seeder:
//
//	func (s *DatabaseSeeder) Run(manager *orm.Manager) error {
//	    runner := seed.NewRunner(manager)
//	    return runner.Call("UserSeeder", "PostSeeder")
//	}
func (r *Runner) Call(names ...string) error {
	for _, name := range names {
		if err := r.Run(name); err != nil {
			return err
		}
	}
	return nil
}

// Ran returns the names of all seeders that were executed.
func (r *Runner) Ran() []string {
	result := make([]string, len(r.ran))
	copy(result, r.ran)
	return result
}

// Seed runs all seeders (or just DatabaseSeeder if registered) using the given manager.
// This is the primary entry point for seeding a database.
//
// Usage:
//
//	manager, _ := orm.NewManager(config)
//	if err := seed.Seed(manager); err != nil {
//	    log.Fatal(err)
//	}
func Seed(manager *orm.Manager) error {
	runner, err := NewRunner(manager)
	if err != nil {
		return err
	}
	return runner.RunAll()
}

// SeedOne runs a single named seeder.
func SeedOne(manager *orm.Manager, name string) error {
	runner, err := NewRunner(manager)
	if err != nil {
		return err
	}
	return runner.Run(name)
}
