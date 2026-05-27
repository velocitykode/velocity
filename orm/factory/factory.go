package factory

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/velocitykode/velocity/orm"
)

// Factory represents a model factory for generating test data.
//
// Concurrency: every field is read/written under mu. mu is a sync.RWMutex
// so generateOne can snapshot the state / sequence maps via RLock while
// Count / State / DefineState / Sequence / Make / Create writers hold
// the write lock. Cross-cutting map mutex sweep, rule #3: the state and
// sequence maps must not be read without the lock, otherwise a concurrent
// DefineState fires "concurrent map read and map write" under -race.
type Factory struct {
	mu          sync.RWMutex
	manager     *orm.Manager
	tableName   string
	definition  func() map[string]interface{}
	states      map[string]map[string]interface{}
	sequences   map[string]func(int) interface{}
	count       int
	activeState string // Track which state to apply
}

// NewFactory creates a new factory for generating test data.
// The manager parameter is required for Create() (database persistence).
// Pass nil if you only use Make() (in-memory generation).
func NewFactory(manager *orm.Manager, tableName string, definition func() map[string]interface{}) *Factory {
	return &Factory{
		manager:    manager,
		tableName:  tableName,
		definition: definition,
		states:     make(map[string]map[string]interface{}),
		sequences:  make(map[string]func(int) interface{}),
		count:      1,
	}
}

// Count sets the number of records to generate.
// Returns an error if n <= 0.
func (f *Factory) Count(n int) *Factory {
	if n <= 0 {
		panic("count must be greater than 0")
	}
	f.mu.Lock()
	f.count = n
	f.mu.Unlock()
	return f
}

// State applies a named state to the factory.
// Panics if the state has not been defined via DefineState.
//
// The presence-check and the activeState write share the same critical
// section so a concurrent DefineState cannot race with the read. Previously
// the presence-check ran without a lock; combined with the lock-held write
// in DefineState this fired "concurrent map read and map write" under
// -race (cross-cutting map mutex sweep, rule #3).
func (f *Factory) State(name string) *Factory {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.states[name]; !exists {
		panic(fmt.Sprintf("unknown state: %s", name))
	}
	f.activeState = name
	return f
}

// Sequence defines a sequential value generator for a field
func (f *Factory) Sequence(field string, generator func(int) interface{}) *Factory {
	f.mu.Lock()
	f.sequences[field] = generator
	f.mu.Unlock()
	return f
}

// DefineState defines a named attribute preset
func (f *Factory) DefineState(name string, attributes map[string]interface{}) {
	f.mu.Lock()
	f.states[name] = attributes
	f.mu.Unlock()
}

// Make generates data without persisting to database
func (f *Factory) Make(overrides ...map[string]interface{}) interface{} {
	f.mu.Lock()
	count := f.count
	activeState := f.activeState
	f.count = 1
	f.activeState = ""
	f.mu.Unlock()

	if count == 1 {
		return f.generateOne(activeState, 0, overrides...)
	}

	results := make([]map[string]interface{}, 0, count)
	for i := 0; i < count; i++ {
		results = append(results, f.generateOne(activeState, i, overrides...))
	}
	return results
}

// Create generates data and persists to database. Takes ctx as the
// first argument so writes participate in the caller's transaction
// when ctx carries a *sql.Tx.
//
// Returns the created record(s) as map[string]interface{} (single) or
// []map[string]interface{} (multiple). Panics if the manager is nil or
// the database connection is unavailable.
func (f *Factory) Create(ctx context.Context, overrides ...map[string]interface{}) interface{} {
	if f.manager == nil {
		panic("ORM manager not set - pass *orm.Manager to NewFactory for database persistence")
	}

	exec := f.manager.DB()
	if exec == nil {
		panic("ORM not connected - manager has no active database connection")
	}

	driver := f.manager.DriverName()
	if driver == "" {
		panic("cannot determine database driver")
	}

	f.mu.Lock()
	count := f.count
	activeState := f.activeState
	f.count = 1
	f.activeState = ""
	f.mu.Unlock()

	if count == 1 {
		return f.persistOne(ctx, exec, driver, activeState, 0, overrides...)
	}

	results := make([]map[string]interface{}, 0, count)
	for i := 0; i < count; i++ {
		results = append(results, f.persistOne(ctx, exec, driver, activeState, i, overrides...))
	}
	return results
}

// generateOne generates a single record's data.
//
// Reads f.states and f.sequences under f.mu.RLock so a concurrent
// DefineState / Sequence cannot race the iteration. The state and
// sequence maps are snapshotted (state via a per-key copy, sequence
// generators via a slice of closures) so the user-supplied generator
// closures and override copies happen outside the lock without
// blocking concurrent factory configuration on slow generators.
func (f *Factory) generateOne(activeState string, index int, overrides ...map[string]interface{}) map[string]interface{} {
	// Start with definition
	data := f.definition()

	// Snapshot state + sequence maps under RLock so the inner loops
	// run without holding the lock. Map iteration vs concurrent
	// assignment is the runtime-fatal race we need to close here.
	var stateCopy map[string]interface{}
	var seqCopy map[string]func(int) interface{}
	f.mu.RLock()
	if activeState != "" {
		if state, exists := f.states[activeState]; exists {
			stateCopy = make(map[string]interface{}, len(state))
			for k, v := range state {
				stateCopy[k] = v
			}
		}
	}
	if len(f.sequences) > 0 {
		seqCopy = make(map[string]func(int) interface{}, len(f.sequences))
		for k, gen := range f.sequences {
			seqCopy[k] = gen
		}
	}
	f.mu.RUnlock()

	// Apply active state from the snapshot.
	for k, v := range stateCopy {
		data[k] = v
	}

	// Apply sequences. 1-based indexing for compatibility with the
	// pre-mutex behaviour.
	for field, generator := range seqCopy {
		data[field] = generator(index + 1)
	}

	// Apply overrides
	if len(overrides) > 0 {
		for k, v := range overrides[0] {
			data[k] = v
		}
	}

	return data
}

// persistOne generates and persists a single record. ctx threads
// through the underlying ExecContext / QueryRowContext call so a
// caller-supplied *sql.Tx in ctx enrolls the insert in the surrounding
// transaction.
func (f *Factory) persistOne(ctx context.Context, exec *sql.DB, driver, activeState string, index int, overrides ...map[string]interface{}) map[string]interface{} {
	data := f.generateOne(activeState, index, overrides...)

	// Build INSERT query
	query, values := buildInsertSQL(f.tableName, data, driver)

	// PostgreSQL uses RETURNING, others use LastInsertId
	if driver == "postgres" {
		query += " RETURNING id"
		var id int64
		err := exec.QueryRowContext(ctx, query, values...).Scan(&id)
		if err != nil {
			panic(fmt.Sprintf("failed to create %s: %v", f.tableName, err))
		}
		data["id"] = id
	} else {
		result, err := exec.ExecContext(ctx, query, values...)
		if err != nil {
			panic(fmt.Sprintf("failed to create %s: %v", f.tableName, err))
		}
		id, err := result.LastInsertId()
		if err == nil {
			data["id"] = id
		}
	}

	return data
}

// quoteIdent quotes a database identifier based on the driver.
func quoteIdent(name, driver string) string {
	switch driver {
	case "mysql", "sqlite", "sqlite3":
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	default: // postgres
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
}

// buildInsertSQL generates driver-specific INSERT statement.
// Columns are sorted for deterministic query output.
func buildInsertSQL(table string, data map[string]interface{}, driver string) (string, []interface{}) {
	// Sort column names for deterministic ordering
	keys := make([]string, 0, len(data))
	for col := range data {
		keys = append(keys, col)
	}
	sort.Strings(keys)

	columns := make([]string, 0, len(data))
	placeholders := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))

	for i, col := range keys {
		columns = append(columns, quoteIdent(col, driver))

		if driver == "postgres" {
			placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
		} else {
			placeholders = append(placeholders, "?")
		}

		values = append(values, data[col])
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		quoteIdent(table, driver),
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "),
	)

	return query, values
}
