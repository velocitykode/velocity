package drivers

import (
	"testing"
	"time"
)

// =============================================================================
// Driver Constructor Tests
// =============================================================================

// The MySQL and PostgreSQL constructor / before-connect tests live with their
// connectors in the orm/mysql and orm/postgres leaf packages. SQLite's
// connector stays in this package (it backs the pure-Go default), so its
// driver-level tests stay here.

func TestNewSQLiteDriver_ReturnsDriverInterface(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"implements Driver interface"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var driver Driver = NewSQLiteDriver()
			if driver == nil {
				t.Error("NewSQLiteDriver() returned nil")
			}
		})
	}
}

// =============================================================================
// ConnectionConfig Tests
// =============================================================================

func TestConnectionConfig_DefaultValues(t *testing.T) {
	tests := []struct {
		name   string
		config ConnectionConfig
	}{
		{
			name:   "empty config has zero values",
			config: ConnectionConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config.Host != "" {
				t.Errorf("Host = %q, want empty", tt.config.Host)
			}
			if tt.config.Port != "" {
				t.Errorf("Port = %q, want empty", tt.config.Port)
			}
			if tt.config.Database != "" {
				t.Errorf("Database = %q, want empty", tt.config.Database)
			}
			if tt.config.MaxIdleConns != 0 {
				t.Errorf("MaxIdleConns = %d, want 0", tt.config.MaxIdleConns)
			}
			if tt.config.MaxOpenConns != 0 {
				t.Errorf("MaxOpenConns = %d, want 0", tt.config.MaxOpenConns)
			}
			if tt.config.LogQueries != false {
				t.Errorf("LogQueries = %v, want false", tt.config.LogQueries)
			}
		})
	}
}

func TestConnectionConfig_WithAllFields(t *testing.T) {
	tests := []struct {
		name   string
		config ConnectionConfig
	}{
		{
			name: "all fields can be set",
			config: ConnectionConfig{
				Driver:             "mysql",
				Host:               "localhost",
				Port:               "3306",
				Database:           "test_db",
				Username:           "user",
				Password:           "pass",
				Charset:            "utf8mb4",
				Collation:          "utf8mb4_unicode_ci",
				Prefix:             "app_",
				Schema:             "public",
				SSLMode:            "require",
				TimeZone:           "UTC",
				MaxIdleConns:       10,
				MaxOpenConns:       100,
				ConnMaxLifetime:    time.Hour,
				ConnMaxIdleTime:    time.Minute * 10,
				LogQueries:         true,
				SlowQueryThreshold: time.Second,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config.Driver != "mysql" {
				t.Errorf("Driver = %q, want %q", tt.config.Driver, "mysql")
			}
			if tt.config.Host != "localhost" {
				t.Errorf("Host = %q, want %q", tt.config.Host, "localhost")
			}
			if tt.config.MaxIdleConns != 10 {
				t.Errorf("MaxIdleConns = %d, want %d", tt.config.MaxIdleConns, 10)
			}
			if tt.config.ConnMaxLifetime != time.Hour {
				t.Errorf("ConnMaxLifetime = %v, want %v", tt.config.ConnMaxLifetime, time.Hour)
			}
			if !tt.config.LogQueries {
				t.Error("LogQueries = false, want true")
			}
		})
	}
}

// =============================================================================
// SelectQuery Tests
// =============================================================================

func TestSelectQuery_DefaultValues(t *testing.T) {
	tests := []struct {
		name  string
		query SelectQuery
	}{
		{
			name:  "empty query has zero values",
			query: SelectQuery{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.query.Table != "" {
				t.Errorf("Table = %q, want empty", tt.query.Table)
			}
			if len(tt.query.Columns) != 0 {
				t.Errorf("Columns length = %d, want 0", len(tt.query.Columns))
			}
			if tt.query.Distinct != false {
				t.Errorf("Distinct = %v, want false", tt.query.Distinct)
			}
			if tt.query.Limit != nil {
				t.Errorf("Limit = %v, want nil", tt.query.Limit)
			}
			if tt.query.LockForUpdate != false {
				t.Errorf("LockForUpdate = %v, want false", tt.query.LockForUpdate)
			}
		})
	}
}

func TestSelectQuery_WithAllFields(t *testing.T) {
	limit := 10
	offset := 20

	tests := []struct {
		name  string
		query SelectQuery
	}{
		{
			name: "all fields can be set",
			query: SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name", "email"},
				Conditions: []Condition{
					{Column: "active", Operator: "=", Value: true, Type: "and"},
				},
				Orders: []Order{
					{Column: "created_at", Direction: "DESC"},
				},
				Groups: []string{"department"},
				Having: []Condition{
					{Column: "count", Operator: ">", Value: 5, Type: "and"},
				},
				Limit:  &limit,
				Offset: &offset,
				Joins: []Join{
					{Type: "LEFT", Table: "roles", On: "users.role_id = roles.id"},
				},
				Distinct:      true,
				LockForUpdate: true,
				SkipLocked:    true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.query.Table != "users" {
				t.Errorf("Table = %q, want %q", tt.query.Table, "users")
			}
			if len(tt.query.Columns) != 3 {
				t.Errorf("Columns length = %d, want %d", len(tt.query.Columns), 3)
			}
			if !tt.query.Distinct {
				t.Error("Distinct = false, want true")
			}
			if *tt.query.Limit != 10 {
				t.Errorf("Limit = %d, want %d", *tt.query.Limit, 10)
			}
			if !tt.query.LockForUpdate {
				t.Error("LockForUpdate = false, want true")
			}
		})
	}
}

// =============================================================================
// Condition Tests
// =============================================================================

func TestCondition_Operators(t *testing.T) {
	tests := []struct {
		name     string
		cond     Condition
		wantOp   string
		wantType string
	}{
		{
			name:     "equals operator",
			cond:     Condition{Column: "id", Operator: "=", Value: 1, Type: "and"},
			wantOp:   "=",
			wantType: "and",
		},
		{
			name:     "not equals operator",
			cond:     Condition{Column: "status", Operator: "!=", Value: "deleted", Type: "and"},
			wantOp:   "!=",
			wantType: "and",
		},
		{
			name:     "greater than operator",
			cond:     Condition{Column: "age", Operator: ">", Value: 18, Type: "and"},
			wantOp:   ">",
			wantType: "and",
		},
		{
			name:     "less than operator",
			cond:     Condition{Column: "price", Operator: "<", Value: 100, Type: "or"},
			wantOp:   "<",
			wantType: "or",
		},
		{
			name:     "IS NULL operator",
			cond:     Condition{Column: "deleted_at", Operator: "IS NULL", Value: nil, Type: "and"},
			wantOp:   "IS NULL",
			wantType: "and",
		},
		{
			name:     "IS NOT NULL operator",
			cond:     Condition{Column: "email", Operator: "IS NOT NULL", Value: nil, Type: "and"},
			wantOp:   "IS NOT NULL",
			wantType: "and",
		},
		{
			name:     "IN operator",
			cond:     Condition{Column: "status", Operator: "IN", Value: []any{"active", "pending"}, Type: "and"},
			wantOp:   "IN",
			wantType: "and",
		},
		{
			name:     "NOT IN operator",
			cond:     Condition{Column: "role", Operator: "NOT IN", Value: []any{"banned", "suspended"}, Type: "and"},
			wantOp:   "NOT IN",
			wantType: "and",
		},
		{
			name:     "BETWEEN operator",
			cond:     Condition{Column: "created_at", Operator: "BETWEEN", Value: []any{"2024-01-01", "2024-12-31"}, Type: "and"},
			wantOp:   "BETWEEN",
			wantType: "and",
		},
		{
			name:     "LIKE operator",
			cond:     Condition{Column: "name", Operator: "LIKE", Value: "%john%", Type: "and"},
			wantOp:   "LIKE",
			wantType: "and",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.cond.Operator != tt.wantOp {
				t.Errorf("Operator = %q, want %q", tt.cond.Operator, tt.wantOp)
			}
			if tt.cond.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", tt.cond.Type, tt.wantType)
			}
		})
	}
}

// =============================================================================
// Order Tests
// =============================================================================

func TestOrder_Directions(t *testing.T) {
	tests := []struct {
		name          string
		order         Order
		wantColumn    string
		wantDirection string
	}{
		{
			name:          "ascending order",
			order:         Order{Column: "name", Direction: "ASC"},
			wantColumn:    "name",
			wantDirection: "ASC",
		},
		{
			name:          "descending order",
			order:         Order{Column: "created_at", Direction: "DESC"},
			wantColumn:    "created_at",
			wantDirection: "DESC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.order.Column != tt.wantColumn {
				t.Errorf("Column = %q, want %q", tt.order.Column, tt.wantColumn)
			}
			if tt.order.Direction != tt.wantDirection {
				t.Errorf("Direction = %q, want %q", tt.order.Direction, tt.wantDirection)
			}
		})
	}
}

// =============================================================================
// Join Tests
// =============================================================================

func TestJoin_Types(t *testing.T) {
	tests := []struct {
		name      string
		join      Join
		wantType  string
		wantTable string
	}{
		{
			name:      "inner join",
			join:      Join{Type: "INNER", Table: "roles", On: "users.role_id = roles.id"},
			wantType:  "INNER",
			wantTable: "roles",
		},
		{
			name:      "left join",
			join:      Join{Type: "LEFT", Table: "profiles", On: "users.id = profiles.user_id"},
			wantType:  "LEFT",
			wantTable: "profiles",
		},
		{
			name:      "right join",
			join:      Join{Type: "RIGHT", Table: "departments", On: "users.dept_id = departments.id"},
			wantType:  "RIGHT",
			wantTable: "departments",
		},
		{
			name:      "full join",
			join:      Join{Type: "FULL", Table: "logs", On: "users.id = logs.user_id"},
			wantType:  "FULL",
			wantTable: "logs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.join.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", tt.join.Type, tt.wantType)
			}
			if tt.join.Table != tt.wantTable {
				t.Errorf("Table = %q, want %q", tt.join.Table, tt.wantTable)
			}
		})
	}
}

// =============================================================================
// Table Tests
// =============================================================================

func TestTable_Structure(t *testing.T) {
	tests := []struct {
		name        string
		table       Table
		wantName    string
		wantColLen  int
		wantIdxLen  int
		wantPrimary string
	}{
		{
			name: "table with columns and indexes",
			table: Table{
				Name: "users",
				Columns: []Column{
					{Name: "id", Type: "INT", Primary: true},
					{Name: "email", Type: "VARCHAR", Unique: true},
				},
				Indexes: []Index{
					{Name: "idx_email", Columns: []string{"email"}, Unique: true},
				},
				Primary: "id",
			},
			wantName:    "users",
			wantColLen:  2,
			wantIdxLen:  1,
			wantPrimary: "id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.table.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", tt.table.Name, tt.wantName)
			}
			if len(tt.table.Columns) != tt.wantColLen {
				t.Errorf("Columns length = %d, want %d", len(tt.table.Columns), tt.wantColLen)
			}
			if len(tt.table.Indexes) != tt.wantIdxLen {
				t.Errorf("Indexes length = %d, want %d", len(tt.table.Indexes), tt.wantIdxLen)
			}
			if tt.table.Primary != tt.wantPrimary {
				t.Errorf("Primary = %q, want %q", tt.table.Primary, tt.wantPrimary)
			}
		})
	}
}

// =============================================================================
// Column Tests
// =============================================================================

func TestColumn_AllTypes(t *testing.T) {
	tests := []struct {
		name   string
		column Column
	}{
		{
			name: "integer column with auto increment",
			column: Column{
				Name:          "id",
				Type:          "INT",
				AutoIncrement: true,
				Primary:       true,
				Nullable:      false,
			},
		},
		{
			name: "varchar column with size",
			column: Column{
				Name:     "email",
				Type:     "VARCHAR",
				Size:     255,
				Nullable: false,
				Unique:   true,
			},
		},
		{
			name: "boolean column with default",
			column: Column{
				Name:     "active",
				Type:     "BOOLEAN",
				Default:  true,
				Nullable: true,
			},
		},
		{
			name: "text column with comment",
			column: Column{
				Name:    "description",
				Type:    "TEXT",
				Comment: "User description",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.column.Name == "" {
				t.Error("Name should not be empty")
			}
			if tt.column.Type == "" {
				t.Error("Type should not be empty")
			}
		})
	}
}

// =============================================================================
// Index Tests
// =============================================================================

func TestIndex_Types(t *testing.T) {
	tests := []struct {
		name       string
		index      Index
		wantUnique bool
	}{
		{
			name: "regular index",
			index: Index{
				Name:    "idx_user_email",
				Columns: []string{"email"},
				Unique:  false,
				Type:    "BTREE",
			},
			wantUnique: false,
		},
		{
			name: "unique index",
			index: Index{
				Name:    "idx_unique_email",
				Columns: []string{"email"},
				Unique:  true,
			},
			wantUnique: true,
		},
		{
			name: "composite index",
			index: Index{
				Name:    "idx_user_role",
				Columns: []string{"user_id", "role_id"},
				Unique:  true,
			},
			wantUnique: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.index.Unique != tt.wantUnique {
				t.Errorf("Unique = %v, want %v", tt.index.Unique, tt.wantUnique)
			}
			if len(tt.index.Columns) == 0 {
				t.Error("Columns should not be empty")
			}
		})
	}
}

// =============================================================================
// Driver Name Tests
// =============================================================================

func TestAllDrivers_DriverName(t *testing.T) {
	tests := []struct {
		name     string
		driver   Driver
		wantName string
	}{
		{"SQLite driver name", NewSQLiteDriver(), "sqlite"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.driver.DriverName()
			if got != tt.wantName {
				t.Errorf("DriverName() = %q, want %q", got, tt.wantName)
			}
		})
	}
}

// =============================================================================
// Grammar Interface Tests
// =============================================================================

func TestAllDrivers_GrammarImplementsInterface(t *testing.T) {
	tests := []struct {
		name   string
		driver Driver
	}{
		{"SQLite grammar implements interface", NewSQLiteDriver()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			grammar := tt.driver.Grammar()
			if grammar == nil {
				t.Error("Grammar() returned nil")
				return
			}

			// Test that all QueryGrammar methods are available
			var _ QueryGrammar = grammar

			// Test specific methods exist and work
			_ = grammar.QuoteIdentifier("test")
			_ = grammar.QuoteString("test")
			_ = grammar.Placeholder(1)
			_ = grammar.CompileHasTable("test")
			_ = grammar.CompileHasColumn("test", "column")
			_ = grammar.CompileDropTable("test")
		})
	}
}

// =============================================================================
// Driver Behavior Tests (Without Connection)
// =============================================================================

func TestAllDrivers_DBReturnsNilBeforeConnect(t *testing.T) {
	tests := []struct {
		name   string
		driver Driver
	}{
		{"SQLite returns nil DB before connect", NewSQLiteDriver()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.driver.DB() != nil {
				t.Error("DB() should return nil before Connect()")
			}
		})
	}
}

func TestAllDrivers_PingFailsBeforeConnect(t *testing.T) {
	tests := []struct {
		name   string
		driver Driver
	}{
		{"SQLite ping fails before connect", NewSQLiteDriver()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.driver.Ping()
			if err == nil {
				t.Error("Ping() should return error before Connect()")
			}
		})
	}
}

func TestAllDrivers_CloseSucceedsBeforeConnect(t *testing.T) {
	tests := []struct {
		name   string
		driver Driver
	}{
		{"SQLite close succeeds before connect", NewSQLiteDriver()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.driver.Close()
			if err != nil {
				t.Errorf("Close() should not return error before Connect(), got %v", err)
			}
		})
	}
}

// =============================================================================
// Helper function for tests
// =============================================================================

func intPtr(i int) *int {
	return &i
}
