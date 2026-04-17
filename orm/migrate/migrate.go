package migrate

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// Migration represents a database schema change
type Migration struct {
	Version     string
	Description string
	Up          func(*Migrator) error
	Down        func(*Migrator) error
}

// Validate checks if the migration is valid
func (m *Migration) Validate() error {
	if m.Version == "" {
		return errors.New("migration version cannot be empty")
	}

	// Version must be YYYYMMDDHHmmss format (14 digits)
	matched, err := regexp.MatchString(`^\d{14}$`, m.Version)
	if err != nil {
		return err
	}
	if !matched {
		return fmt.Errorf("migration version must be YYYYMMDDHHmmss format, got: %s", m.Version)
	}

	if m.Up == nil {
		return errors.New("migration Up function cannot be nil")
	}

	return nil
}

// MigrationRegistry is a global registry for all migrations
type MigrationRegistry struct {
	migrations []Migration
	sorted     bool
	mu         sync.RWMutex
}

var (
	globalRegistry = &MigrationRegistry{
		migrations: make([]Migration, 0),
	}
)

// Register adds a migration to the global registry
func Register(migration *Migration) {
	if err := migration.Validate(); err != nil {
		panic(fmt.Sprintf("invalid migration: %v", err))
	}

	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()

	// Check for duplicate version
	for _, m := range globalRegistry.migrations {
		if m.Version == migration.Version {
			panic(fmt.Sprintf("duplicate migration version: %s", migration.Version))
		}
	}

	globalRegistry.migrations = append(globalRegistry.migrations, *migration)
	globalRegistry.sorted = false
}

// All returns all registered migrations sorted by version
func (r *MigrationRegistry) All() []Migration {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.sorted {
		sort.Slice(r.migrations, func(i, j int) bool {
			return r.migrations[i].Version < r.migrations[j].Version
		})
		r.sorted = true
	}

	// Return a copy
	result := make([]Migration, len(r.migrations))
	copy(result, r.migrations)
	return result
}

// Find returns a migration by version
func (r *MigrationRegistry) Find(version string) (*Migration, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, m := range r.migrations {
		if m.Version == version {
			return &m, nil
		}
	}

	return nil, fmt.Errorf("migration not found: %s", version)
}

// Pending returns migrations that haven't been applied yet
func (r *MigrationRegistry) Pending(db *sql.DB, driver string) ([]Migration, error) {
	migrator := NewMigrator(db, driver)

	// Get applied migrations
	applied, err := migrator.getAppliedMigrations()
	if err != nil {
		return nil, err
	}

	// Build set of applied versions
	appliedSet := make(map[string]bool)
	for _, version := range applied {
		appliedSet[version] = true
	}

	// Find pending migrations
	all := r.All()
	pending := make([]Migration, 0)
	for _, m := range all {
		if !appliedSet[m.Version] {
			pending = append(pending, m)
		}
	}

	return pending, nil
}

// Package-level convenience functions

// All returns all registered migrations
func All() []Migration {
	return globalRegistry.All()
}

// Find returns a migration by version
func Find(version string) (*Migration, error) {
	return globalRegistry.Find(version)
}

// Pending returns migrations not yet applied
func Pending(db *sql.DB, driver string) ([]Migration, error) {
	return globalRegistry.Pending(db, driver)
}

// Up runs all pending migrations using the default ORM connection
func Up() error {
	// Import orm package at runtime to avoid circular dependency
	// Users must ensure orm.Init() is called before migrate.Up()
	return errors.New("velocity/orm: up requires ORM integration - use migrator.Up() directly")
}

// Down rolls back the last N batches using the default ORM connection
func Down(steps int) error {
	return errors.New("velocity/orm: down requires ORM integration - use migrator.Down() directly")
}

// Status returns migration status using the default ORM connection
func Status() ([]MigrationStatus, error) {
	return nil, errors.New("velocity/orm: status requires ORM integration - use migrator.Status() directly")
}
