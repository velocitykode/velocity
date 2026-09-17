package seed

import (
	"context"
	"errors"
	"fmt"

	"github.com/velocitykode/velocity/orm"
)

// Runner executes seeders against one database connection and records which
// of them completed. A Runner is not safe for concurrent use.
type Runner struct {
	db  *orm.Manager
	ran []string
}

// NewRunner returns a Runner bound to db. It fails when db is nil or has no
// open connection, so a misconfigured environment is reported before any
// seeder runs.
func NewRunner(db *orm.Manager) (*Runner, error) {
	if db == nil {
		return nil, errors.New("seed: manager cannot be nil")
	}
	if db.DB() == nil {
		return nil, errors.New("seed: manager has no active database connection")
	}
	return &Runner{db: db}, nil
}

// Run executes seeders in order. It stops at the first failure and returns
// that error wrapped with the seeder's name. Before each seeder it checks
// ctx, so a cancelled context ends the run between seeders instead of
// starting the next one.
func (r *Runner) Run(ctx context.Context, seeders ...Seeder) error {
	for _, s := range seeders {
		if s == nil {
			return errors.New("seed: seeder cannot be nil")
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("seed: seeding stopped before %s: %w", s.Name(), err)
		}
		if err := s.Run(ctx, r.db); err != nil {
			return fmt.Errorf("seed: seeder %s failed: %w", s.Name(), err)
		}
		r.ran = append(r.ran, s.Name())
	}
	return nil
}

// Ran returns the names of the seeders that completed, in execution order.
// The slice is a copy.
func (r *Runner) Ran() []string {
	out := make([]string, len(r.ran))
	copy(out, r.ran)
	return out
}
