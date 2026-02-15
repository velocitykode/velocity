// Package seed provides a database seeding system for populating
// development and staging databases with sample data.
//
// Seeders are registered globally via Register() and executed via
// a Runner or the convenience Seed() function. Seeders use factories
// from the orm/factory package to generate realistic data.
//
// Basic usage:
//
//	// Define a seeder
//	type UserSeeder struct{}
//	func (s *UserSeeder) Name() string { return "UserSeeder" }
//	func (s *UserSeeder) Run(manager *orm.Manager) error {
//	    f := factory.NewFactory(manager, "users", func() map[string]interface{}{
//	        return map[string]interface{}{"name": factory.F().Name()}
//	    })
//	    f.Count(10).Create()
//	    return nil
//	}
//
//	// Register in init()
//	func init() { seed.Register(&UserSeeder{}) }
//
//	// Run all seeders
//	seed.Seed(manager)
package seed

import (
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/orm"
)

// Seeder defines the contract for a database seeder.
// Each seeder populates one or more tables with development/staging data.
type Seeder interface {
	// Name returns a unique identifier for this seeder.
	// Convention: "UserSeeder", "PostSeeder", "DatabaseSeeder"
	Name() string

	// Run executes the seeder. The manager provides database access.
	// Seeders should use factories from orm/factory or direct orm.Save()
	// to insert data.
	Run(manager *orm.Manager) error
}

// SeederRegistry is a global registry for all seeders.
type SeederRegistry struct {
	seeders map[string]Seeder
	order   []string // preserves registration order
	mu      sync.RWMutex
}

var globalRegistry = &SeederRegistry{
	seeders: make(map[string]Seeder),
	order:   make([]string, 0),
}

// Register adds a seeder to the global registry.
// Panics if the seeder is nil, has an empty name, or a duplicate name is registered.
// Typically called from init() functions.
func Register(seeder Seeder) {
	if seeder == nil {
		panic("seed: seeder cannot be nil")
	}
	name := seeder.Name()
	if name == "" {
		panic("seed: seeder name cannot be empty")
	}

	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()

	if _, exists := globalRegistry.seeders[name]; exists {
		panic(fmt.Sprintf("seed: duplicate seeder name: %s", name))
	}

	globalRegistry.seeders[name] = seeder
	globalRegistry.order = append(globalRegistry.order, name)
}

// All returns all registered seeders in registration order.
func All() []Seeder {
	return globalRegistry.All()
}

// Find returns a seeder by name.
func Find(name string) (Seeder, error) {
	return globalRegistry.Find(name)
}

// Reset clears all registered seeders. Used in tests.
func Reset() {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.seeders = make(map[string]Seeder)
	globalRegistry.order = make([]string, 0)
}

// All returns all registered seeders in registration order.
func (r *SeederRegistry) All() []Seeder {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Seeder, 0, len(r.order))
	for _, name := range r.order {
		result = append(result, r.seeders[name])
	}
	return result
}

// Find returns a seeder by name.
func (r *SeederRegistry) Find(name string) (Seeder, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seeder, exists := r.seeders[name]
	if !exists {
		return nil, fmt.Errorf("seed: seeder not found: %s", name)
	}
	return seeder, nil
}
