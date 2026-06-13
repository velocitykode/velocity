package migrate

import "testing"

// TestSQLColumnType pins the consolidated per-driver/per-context type matrix
// that sqlColumnType is the single source of truth for. The managed timestamp
// default is the only thing the DDL context gates; everything else is identical
// across createTable and addColumn.
func TestSQLColumnType(t *testing.T) {
	tests := []struct {
		name      string
		driver    string
		colType   string
		length    int
		precision int
		scale     int
		dims      int
		nullable  bool
		hasDef    bool
		ctx       ddlContext
		want      string
	}{
		// SQLite scalars.
		{"sqlite integer", "sqlite", "integer", 0, 0, 0, 0, false, false, ddlCreateTable, "INTEGER"},
		{"sqlite biginteger", "sqlite", "biginteger", 0, 0, 0, 0, false, false, ddlCreateTable, "INTEGER"},
		{"sqlite smallinteger", "sqlite", "smallinteger", 0, 0, 0, 0, false, false, ddlCreateTable, "INTEGER"},
		{"sqlite boolean", "sqlite", "boolean", 0, 0, 0, 0, false, false, ddlCreateTable, "INTEGER"},
		{"sqlite string", "sqlite", "string", 100, 0, 0, 0, false, false, ddlCreateTable, "VARCHAR(100)"},
		{"sqlite text", "sqlite", "text", 0, 0, 0, 0, false, false, ddlCreateTable, "TEXT"},
		{"sqlite binary", "sqlite", "binary", 0, 0, 0, 0, false, false, ddlCreateTable, "BLOB"},
		{"sqlite date", "sqlite", "date", 0, 0, 0, 0, false, false, ddlCreateTable, "DATE"},
		{"sqlite uuid", "sqlite", "uuid", 0, 0, 0, 0, false, false, ddlCreateTable, "TEXT"},
		{"sqlite json", "sqlite", "json", 0, 0, 0, 0, false, false, ddlCreateTable, "TEXT"},
		{"sqlite jsonb", "sqlite", "jsonb", 0, 0, 0, 0, false, false, ddlCreateTable, "TEXT"},
		{"sqlite decimal", "sqlite", "decimal", 0, 5, 2, 0, false, false, ddlCreateTable, "NUMERIC(5,2)"},
		{"sqlite unknown", "sqlite", "garbage", 0, 0, 0, 0, false, false, ddlCreateTable, "TEXT"},
		// SQLite timestamp: managed default only in createTable, never on ADD COLUMN.
		{"sqlite ts create non-null", "sqlite", "timestamp", 0, 0, 0, 0, false, false, ddlCreateTable, "DATETIME DEFAULT CURRENT_TIMESTAMP"},
		{"sqlite ts addcol non-null", "sqlite", "timestamp", 0, 0, 0, 0, false, false, ddlAddColumn, "DATETIME"},
		{"sqlite ts create nullable", "sqlite", "timestamptz", 0, 0, 0, 0, true, false, ddlCreateTable, "DATETIME"},
		{"sqlite ts create has-default", "sqlite", "timestamp", 0, 0, 0, 0, false, true, ddlCreateTable, "DATETIME"},
		// SQLite decimal with the no-precision sentinel degrades to TEXT (the
		// builder path); an explicit precision-0 decimal stays NUMERIC(0,0).
		{"sqlite decimal no precision", "sqlite", "decimal", 0, decimalPrecisionUnset, 0, 0, false, false, ddlAddColumn, "TEXT"},
		{"sqlite decimal precision 0", "sqlite", "decimal", 0, 0, 0, 0, false, false, ddlCreateTable, "NUMERIC(0,0)"},

		// Postgres scalars.
		{"pg integer", "postgres", "integer", 0, 0, 0, 0, false, false, ddlCreateTable, "INTEGER"},
		{"pg biginteger", "postgres", "biginteger", 0, 0, 0, 0, false, false, ddlCreateTable, "BIGINT"},
		{"pg smallinteger", "postgres", "smallinteger", 0, 0, 0, 0, false, false, ddlCreateTable, "SMALLINT"},
		{"pg binary", "postgres", "binary", 0, 0, 0, 0, false, false, ddlCreateTable, "BYTEA"},
		{"pg boolean", "postgres", "boolean", 0, 0, 0, 0, false, false, ddlCreateTable, "BOOLEAN"},
		{"pg uuid", "postgres", "uuid", 0, 0, 0, 0, false, false, ddlCreateTable, "UUID"},
		{"pg json", "postgres", "json", 0, 0, 0, 0, false, false, ddlCreateTable, "JSON"},
		{"pg jsonb", "postgres", "jsonb", 0, 0, 0, 0, false, false, ddlCreateTable, "JSONB"},
		{"pg decimal", "postgres", "decimal", 0, 8, 4, 0, false, false, ddlCreateTable, "NUMERIC(8,4)"},
		{"pg vector", "postgres", "vector", 0, 0, 0, 1536, false, false, ddlCreateTable, "vector(1536)"},
		{"pg unknown", "postgres", "garbage", 0, 0, 0, 0, false, false, ddlCreateTable, "TEXT"},
		// Postgres timestamp: managed default in both contexts.
		{"pg ts create", "postgres", "timestamp", 0, 0, 0, 0, false, false, ddlCreateTable, "TIMESTAMP DEFAULT NOW()"},
		{"pg ts addcol", "postgres", "timestamp", 0, 0, 0, 0, false, false, ddlAddColumn, "TIMESTAMP DEFAULT NOW()"},
		{"pg tstz addcol", "postgres", "timestamptz", 0, 0, 0, 0, false, false, ddlAddColumn, "TIMESTAMPTZ DEFAULT NOW()"},
		{"pg ts nullable", "postgres", "timestamp", 0, 0, 0, 0, true, false, ddlCreateTable, "TIMESTAMP"},

		// MySQL scalars.
		{"mysql integer", "mysql", "integer", 0, 0, 0, 0, false, false, ddlCreateTable, "INT"},
		{"mysql biginteger", "mysql", "biginteger", 0, 0, 0, 0, false, false, ddlCreateTable, "BIGINT"},
		{"mysql smallinteger", "mysql", "smallinteger", 0, 0, 0, 0, false, false, ddlCreateTable, "SMALLINT"},
		{"mysql binary", "mysql", "binary", 0, 0, 0, 0, false, false, ddlCreateTable, "LONGBLOB"},
		{"mysql uuid", "mysql", "uuid", 0, 0, 0, 0, false, false, ddlCreateTable, "CHAR(36)"},
		{"mysql json", "mysql", "json", 0, 0, 0, 0, false, false, ddlCreateTable, "JSON"},
		{"mysql jsonb", "mysql", "jsonb", 0, 0, 0, 0, false, false, ddlCreateTable, "JSON"},
		{"mysql decimal", "mysql", "decimal", 0, 10, 2, 0, false, false, ddlCreateTable, "DECIMAL(10,2)"},
		{"mysql unknown", "mysql", "garbage", 0, 0, 0, 0, false, false, ddlCreateTable, "TEXT"},
		// MySQL timestamp: managed default in both contexts; timestamptz aliases timestamp.
		{"mysql ts create", "mysql", "timestamp", 0, 0, 0, 0, false, false, ddlCreateTable, "TIMESTAMP DEFAULT CURRENT_TIMESTAMP"},
		{"mysql tstz addcol", "mysql", "timestamptz", 0, 0, 0, 0, false, false, ddlAddColumn, "TIMESTAMP DEFAULT CURRENT_TIMESTAMP"},
		{"mysql ts nullable", "mysql", "timestamp", 0, 0, 0, 0, true, false, ddlCreateTable, "TIMESTAMP"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sqlColumnType(tt.driver, tt.colType, tt.length, tt.precision, tt.scale, tt.dims, tt.nullable, tt.hasDef, tt.ctx)
			if got != tt.want {
				t.Errorf("sqlColumnType(%q, %q, ...) = %q, want %q", tt.driver, tt.colType, got, tt.want)
			}
		})
	}
}
