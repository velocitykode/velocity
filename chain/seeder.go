package chain

import (
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/orm/seed"
)

// Seeders is the registry of an application's database seeders. It is
// populated through the Seeders chain step (App.Seeders) and the optional
// SeederModule interface, and consumed by `vel db seed` and
// `vel migrate fresh --seed`.
//
// Registration order is execution order: list parents before the seeders
// that depend on their rows. Names must be unique; `vel db seed --only
// <name>` selects a single seeder by it.
type Seeders struct {
	seeders map[string]seed.Seeder
	ordered []seed.Seeder // preserves insertion order = execution order
}

// NewSeeders creates an empty Seeders registry.
// Called from the root velocity package during bootstrap.
func NewSeeders() *Seeders {
	return &Seeders{
		seeders: make(map[string]seed.Seeder),
	}
}

// Add registers one or more seeders in the order given. It panics with a
// RegistrationError if any seeder is nil, has an empty name, or repeats a
// name already registered: all three are wiring mistakes in application
// code, not runtime conditions.
func (r *Seeders) Add(seeders ...seed.Seeder) {
	for _, s := range seeders {
		if s == nil {
			panic(contract.NewRegistrationError("seeders", "cannot register nil seeder"))
		}
		name := s.Name()
		if name == "" {
			panic(contract.NewRegistrationError("seeders", "seeder name cannot be empty"))
		}
		if _, exists := r.seeders[name]; exists {
			panic(contract.NewRegistrationError("seeders", "duplicate seeder name: "+name))
		}
		r.seeders[name] = s
		r.ordered = append(r.ordered, s)
	}
}

// Get returns the seeder with the given name and true, or nil and false.
func (r *Seeders) Get(name string) (seed.Seeder, bool) {
	s, ok := r.seeders[name]
	return s, ok
}

// All returns all registered seeders in registration order. The slice is a
// copy.
func (r *Seeders) All() []seed.Seeder {
	out := make([]seed.Seeder, len(r.ordered))
	copy(out, r.ordered)
	return out
}
