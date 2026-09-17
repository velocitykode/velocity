package console

import (
	"context"
	"fmt"
	"strings"

	"github.com/velocitykode/prism"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/seed"
)

// SeedOptions holds flags for the db seed command.
type SeedOptions struct {
	// Only names a single seeder to run instead of the whole registered list.
	Only string
}

// Seed runs the application's seeders in the order they were registered,
// printing one line per completed seeder, and stops at the first failure.
//
// Like DBWipe, this is the unguarded programmatic primitive: no environment
// check or confirmation. The production gate lives in the `vel db seed` CLI
// command. ctx is threaded into every seeder; cancelling it stops the run
// between seeders.
func Seed(ctx context.Context, db *orm.Manager, seeders []seed.Seeder, opts ...SeedOptions) error {
	if db == nil {
		prism.Warning("No database configured (DB_CONNECTION not set), skipping seeding")
		return nil
	}

	var opt SeedOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	// An explicit selection is validated before the empty-registry shortcut:
	// `--only role` against a forgotten kernel.go must fail, not report Done,
	// or a bootstrap script continues without the rows it asked for.
	if opt.Only != "" {
		s, ok := findSeeder(seeders, opt.Only)
		if !ok {
			return fmt.Errorf("velocity/console: seeder %q is not registered (registered: %s)", opt.Only, registeredList(seeders))
		}
		seeders = []seed.Seeder{s}
	}

	if len(seeders) == 0 {
		prism.Warning("No seeders registered")
		prism.Muted("Create one with: vel gen seeder <Name>, then add it in database/seeders/kernel.go")
		return nil
	}

	runner, err := seed.NewRunner(db)
	if err != nil {
		return fmt.Errorf("velocity/console: seeding failed: %w", err)
	}

	prism.Info("Seeding database...")

	for _, s := range seeders {
		if err := runner.Run(ctx, s); err != nil {
			return fmt.Errorf("velocity/console: seeding failed: %w", err)
		}
		prism.Success(s.Name())
	}

	prism.Newline()
	prism.Success("Done")
	return nil
}

func findSeeder(seeders []seed.Seeder, name string) (seed.Seeder, bool) {
	for _, s := range seeders {
		if s.Name() == name {
			return s, true
		}
	}
	return nil, false
}

// registeredList renders the registered seeder names for an error message,
// or "none" when the registry is empty.
func registeredList(seeders []seed.Seeder) string {
	if len(seeders) == 0 {
		return "none"
	}
	names := make([]string, len(seeders))
	for i, s := range seeders {
		names[i] = s.Name()
	}
	return strings.Join(names, ", ")
}
