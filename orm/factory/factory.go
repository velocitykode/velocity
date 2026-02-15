package factory

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/velocitykode/velocity/orm"
)

// Factory represents a model factory for generating test data
type Factory struct {
	mu          sync.Mutex
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
func (f *Factory) State(name string) *Factory {
	if _, exists := f.states[name]; !exists {
		panic(fmt.Sprintf("unknown state: %s", name))
	}
	f.mu.Lock()
	f.activeState = name
	f.mu.Unlock()
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

// Create generates data and persists to database.
// Returns the created record(s) as map[string]interface{} (single) or
// []map[string]interface{} (multiple). Panics if the manager is nil or
// the database connection is unavailable.
func (f *Factory) Create(overrides ...map[string]interface{}) interface{} {
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
		return f.persistOne(exec, driver, activeState, 0, overrides...)
	}

	results := make([]map[string]interface{}, 0, count)
	for i := 0; i < count; i++ {
		results = append(results, f.persistOne(exec, driver, activeState, i, overrides...))
	}
	return results
}

// generateOne generates a single record's data
func (f *Factory) generateOne(activeState string, index int, overrides ...map[string]interface{}) map[string]interface{} {
	// Start with definition
	data := f.definition()

	// Apply active state
	if activeState != "" {
		if state, exists := f.states[activeState]; exists {
			for k, v := range state {
				data[k] = v
			}
		}
	}

	// Apply sequences
	for field, generator := range f.sequences {
		data[field] = generator(index + 1) // 1-based indexing for sequences
	}

	// Apply overrides
	if len(overrides) > 0 {
		for k, v := range overrides[0] {
			data[k] = v
		}
	}

	return data
}

// persistOne generates and persists a single record
func (f *Factory) persistOne(exec orm.QueryExecutor, driver, activeState string, index int, overrides ...map[string]interface{}) map[string]interface{} {
	data := f.generateOne(activeState, index, overrides...)

	// Build INSERT query
	query, values := buildInsertSQL(f.tableName, data, driver)

	// PostgreSQL uses RETURNING, others use LastInsertId
	if driver == "postgres" {
		query += " RETURNING id"
		var id int64
		err := exec.QueryRow(query, values...).Scan(&id)
		if err != nil {
			panic(fmt.Sprintf("failed to create %s: %v", f.tableName, err))
		}
		data["id"] = id
	} else {
		result, err := exec.Exec(query, values...)
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
