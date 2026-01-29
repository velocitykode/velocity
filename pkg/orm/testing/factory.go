package testing

import (
	"database/sql"
	"fmt"
	"sync"

	"github.com/brianvoe/gofakeit/v6"
	"github.com/velocitykode/velocity/pkg/orm"
)

var (
	fakerInstance *gofakeit.Faker
	fakerOnce     sync.Once
)

// Factory represents a model factory for generating test data
type Factory struct {
	tableName   string
	definition  func() map[string]interface{}
	states      map[string]map[string]interface{}
	sequences   map[string]func(int) interface{}
	count       int
	activeState string // Track which state to apply
}

// NewFactory creates a new factory for generating test data
func NewFactory(tableName string, definition func() map[string]interface{}) *Factory {
	return &Factory{
		tableName:  tableName,
		definition: definition,
		states:     make(map[string]map[string]interface{}),
		sequences:  make(map[string]func(int) interface{}),
		count:      1,
	}
}

// Count sets the number of records to generate
func (f *Factory) Count(n int) *Factory {
	if n <= 0 {
		panic("count must be greater than 0")
	}
	f.count = n
	return f
}

// State applies a named state to the factory
func (f *Factory) State(name string) *Factory {
	if _, exists := f.states[name]; !exists {
		panic(fmt.Sprintf("unknown state: %s", name))
	}
	f.activeState = name
	return f
}

// Sequence defines a sequential value generator for a field
func (f *Factory) Sequence(field string, generator func(int) interface{}) *Factory {
	f.sequences[field] = generator
	return f
}

// DefineState defines a named attribute preset
func (f *Factory) DefineState(name string, attributes map[string]interface{}) {
	f.states[name] = attributes
}

// Make generates data without persisting to database
func (f *Factory) Make(overrides ...map[string]interface{}) interface{} {
	if f.count == 1 {
		return f.generateOne(0, overrides...)
	}

	results := make([]map[string]interface{}, 0, f.count)
	for i := 0; i < f.count; i++ {
		results = append(results, f.generateOne(i, overrides...))
	}
	return results
}

// Create generates data and persists to database
func (f *Factory) Create(overrides ...map[string]interface{}) interface{} {
	db := orm.DB()
	if db == nil {
		panic("ORM not initialized - call orm.Init() before using factories")
	}

	driver := orm.GetDriver()
	if driver == "" {
		panic("cannot determine database driver")
	}

	// Capture count for this call
	count := f.count

	// Reset count and state for next call (defer to ensure reset even on panic)
	defer func() {
		f.count = 1
		f.activeState = ""
	}()

	if count == 1 {
		return f.persistOne(db, driver, 0, overrides...)
	}

	results := make([]map[string]interface{}, 0, count)
	for i := 0; i < count; i++ {
		results = append(results, f.persistOne(db, driver, i, overrides...))
	}
	return results
}

// generateOne generates a single record's data
func (f *Factory) generateOne(index int, overrides ...map[string]interface{}) map[string]interface{} {
	// Start with definition
	data := f.definition()

	// Apply active state
	if f.activeState != "" {
		if state, exists := f.states[f.activeState]; exists {
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
func (f *Factory) persistOne(db *sql.DB, driver string, index int, overrides ...map[string]interface{}) map[string]interface{} {
	data := f.generateOne(index, overrides...)

	// Build INSERT query
	query, values := buildInsertSQL(f.tableName, data, driver)

	// PostgreSQL uses RETURNING, others use LastInsertId
	if driver == "postgres" {
		query += " RETURNING id"
		var id int64
		err := db.QueryRow(query, values...).Scan(&id)
		if err != nil {
			panic(fmt.Sprintf("failed to create %s: %v", f.tableName, err))
		}
		data["id"] = id
	} else {
		result, err := db.Exec(query, values...)
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

// buildInsertSQL generates driver-specific INSERT statement
func buildInsertSQL(table string, data map[string]interface{}, driver string) (string, []interface{}) {
	columns := make([]string, 0, len(data))
	placeholders := make([]string, 0, len(data))
	values := make([]interface{}, 0, len(data))

	i := 1
	for col, val := range data {
		columns = append(columns, col)

		if driver == "postgres" {
			placeholders = append(placeholders, fmt.Sprintf("$%d", i))
		} else {
			// MySQL and SQLite use ?
			placeholders = append(placeholders, "?")
		}

		values = append(values, val)
		i++
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		table,
		joinStrings(columns, ", "),
		joinStrings(placeholders, ", "),
	)

	return query, values
}

// joinStrings joins a slice of strings with a separator
func joinStrings(arr []string, sep string) string {
	if len(arr) == 0 {
		return ""
	}
	result := arr[0]
	for i := 1; i < len(arr); i++ {
		result += sep + arr[i]
	}
	return result
}

// Faker returns the global faker instance
func Faker() *gofakeit.Faker {
	fakerOnce.Do(func() {
		fakerInstance = gofakeit.New(0) // Seed with 0 for consistent testing, or use time for randomness
	})
	return fakerInstance
}

// F is a convenience alias for Faker()
func F() *gofakeit.Faker {
	return Faker()
}
