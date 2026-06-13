// Package postgres is the per-driver leaf that backs Velocity's ORM with
// PostgreSQL via lib/pq. It lives outside orm/drivers so the ORM core never
// pulls in lib/pq: importing this package (directly or via orm/standard)
// registers the "postgres" driver factory and attaches lib/pq only to the
// binaries that ask for it.
//
//	import _ "github.com/velocitykode/velocity/orm/postgres"
//
// The driver self-registers into orm.Drivers() from init(); use New for a
// connected driver without going through the registry.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/lib/pq"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/drivers"
)

// init registers the postgres driver into the canonical ORM registry. The
// driver lives in this leaf package so the orm root never pulls in lib/pq;
// importing this package (directly or via orm/standard) wires the "postgres"
// factory.
func init() {
	orm.Drivers().Register("postgres", func(_ context.Context, cfg drivers.ConnectionConfig) (drivers.Driver, error) {
		return New(cfg)
	})
}

// New constructs a connected PostgreSQL driver from cfg for standalone use
// without going through the ORM driver registry. It returns the same driver
// the registry path produces, so both routes are equivalent.
func New(cfg drivers.ConnectionConfig) (drivers.Driver, error) {
	d := &PostgresDriver{}
	if err := d.Connect(cfg); err != nil {
		return nil, err
	}
	return d, nil
}

// escapePgDSNValue escapes a value for use in a PostgreSQL key=value DSN string.
//
// lib/pq / libpq key=value DSNs quote values containing whitespace by wrapping
// them in single quotes. Inside a single-quoted value, embedded single quotes
// and backslashes must be escaped with a backslash. All other characters are
// left intact. Empty values are emitted as empty single-quoted strings.
//
// References: https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING
func escapePgDSNValue(val string) string {
	if val == "" {
		return "''"
	}
	// Quoting is required when the value contains whitespace, '\'', backslash,
	// or the '=' used as the key/value separator.
	needsQuoting := false
	for _, r := range val {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\'' || r == '\\' || r == '=' {
			needsQuoting = true
			break
		}
	}
	if !needsQuoting {
		return val
	}
	// Inside single-quoted values, escape backslash first, then single quote.
	escaped := strings.ReplaceAll(val, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `'`, `\'`)
	return "'" + escaped + "'"
}

// redactDSNPassword replaces the password= value in a libpq key=value DSN
// string with "[REDACTED]" so DSN contents can be safely included in error
// messages and logs.
func redactDSNPassword(dsn string) string {
	// The DSN is a space-separated list of key=value pairs. The password
	// value may or may not be single-quoted depending on its contents.
	idx := strings.Index(dsn, "password=")
	if idx == -1 {
		return dsn
	}
	start := idx + len("password=")
	if start >= len(dsn) {
		return dsn[:start] + "[REDACTED]"
	}
	var end int
	if dsn[start] == '\'' {
		// Quoted value: scan past escape sequences until the closing quote.
		end = start + 1
		for end < len(dsn) {
			if dsn[end] == '\\' && end+1 < len(dsn) {
				end += 2
				continue
			}
			if dsn[end] == '\'' {
				end++
				break
			}
			end++
		}
	} else {
		// Unquoted value: read until the next whitespace.
		end = start
		for end < len(dsn) && dsn[end] != ' ' && dsn[end] != '\t' {
			end++
		}
	}
	return dsn[:idx] + "password=[REDACTED]" + dsn[end:]
}

// PostgresDriver implements the drivers.Driver interface for PostgreSQL.
type PostgresDriver struct {
	drivers.BaseDriver
}

// NewPostgresDriver creates a new (unconnected) PostgreSQL driver instance.
func NewPostgresDriver() drivers.Driver {
	return &PostgresDriver{}
}

// resolveSSLMode returns the effective sslmode for a Postgres connection.
//
// Precedence:
//  1. Config.SSLMode, if explicitly set by the caller.
//  2. DB_SSL_MODE env var, if set. Allows deployment-level opt-out.
//  3. "require", the secure default. Applications must explicitly opt out
//     if they need to connect to an unencrypted server.
//
// Callers that need to disable TLS for local development must set
// Config.SSLMode="disable" (DB_SSL_MODE is read at config time in
// ConfigFromEnv, not by the driver).
func resolveSSLMode(configured string) string {
	if configured != "" {
		return configured
	}
	return "require"
}

// Connect establishes a connection to a PostgreSQL database.
func (d *PostgresDriver) Connect(config drivers.ConnectionConfig) error {
	d.Config = config

	// Build DSN (PostgreSQL connection string)
	// Escape values to handle special characters (spaces, quotes, backslashes)
	dsn := fmt.Sprintf("host=%s port=%s user=%s dbname=%s",
		escapePgDSNValue(config.Host),
		escapePgDSNValue(config.Port),
		escapePgDSNValue(config.Username),
		escapePgDSNValue(config.Database),
	)

	// Add password only if provided
	if config.Password != "" {
		dsn += " password=" + escapePgDSNValue(config.Password)
	}

	// sslmode defaults to require, secure by default.
	dsn += " sslmode=" + escapePgDSNValue(resolveSSLMode(config.SSLMode))

	if config.TimeZone != "" {
		dsn += " TimeZone=" + escapePgDSNValue(config.TimeZone)
	}

	if config.Schema != "" {
		dsn += " search_path=" + escapePgDSNValue(config.Schema)
	}

	if err := d.OpenAndPing("postgres", dsn); err != nil {
		return fmt.Errorf("velocity/orm: postgres connect failed (dsn=%q): %w", redactDSNPassword(dsn), err)
	}
	return nil
}

// HasTable checks if a table exists
func (d *PostgresDriver) HasTable(name string) bool {
	sql := d.Grammar().CompileHasTable(name)
	var exists bool
	err := d.DB().QueryRow(sql, name).Scan(&exists)
	return err == nil && exists
}

// HasColumn checks if a column exists in a table
func (d *PostgresDriver) HasColumn(table, column string) bool {
	sql := d.Grammar().CompileHasColumn(table, column)
	var count int
	err := d.DB().QueryRow(sql, table, column).Scan(&count)
	return err == nil && count > 0
}

func (d *PostgresDriver) introspectionSchema() string {
	schema := d.Config.Schema
	if schema == "" {
		return "public"
	}
	schema, _, _ = strings.Cut(schema, ",")
	return strings.TrimSpace(schema)
}

// ListTables returns user tables in the configured PostgreSQL schema.
func (d *PostgresDriver) ListTables(ctx context.Context) ([]string, error) {
	schema := d.introspectionSchema()
	grammar, ok := d.Grammar().(drivers.IntrospectionGrammar)
	if !ok {
		return nil, fmt.Errorf("postgres grammar does not support schema introspection")
	}
	rows, err := d.QueryContext(ctx, grammar.CompileListTables(), schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := make([]string, 0)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

// DescribeTable returns column metadata for a PostgreSQL table in ordinal order.
func (d *PostgresDriver) DescribeTable(ctx context.Context, table string) ([]drivers.ColumnSchema, error) {
	if err := drivers.ValidateSchemaIdentifier(table); err != nil {
		return nil, err
	}
	schema := d.introspectionSchema()

	grammar, ok := d.Grammar().(drivers.IntrospectionGrammar)
	if !ok {
		return nil, fmt.Errorf("postgres grammar does not support schema introspection")
	}
	rows, err := d.QueryContext(ctx, grammar.CompileDescribeTable(table), schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := make([]drivers.ColumnSchema, 0)
	for rows.Next() {
		var name string
		var dataType string
		var nullable string
		var defaultValue sql.NullString
		var primaryKey bool

		if err := rows.Scan(&name, &dataType, &nullable, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		var defaultPtr *string
		if defaultValue.Valid {
			value := defaultValue.String
			defaultPtr = &value
		}
		columns = append(columns, drivers.ColumnSchema{
			Name:       name,
			DataType:   dataType,
			Nullable:   nullable == "YES",
			Default:    defaultPtr,
			PrimaryKey: primaryKey,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("postgres table %q not found", table)
	}
	return columns, nil
}

// CreateTable creates a new table
func (d *PostgresDriver) CreateTable(name string, definition func(*drivers.Table)) error {
	return d.CreateTableWith(d.Grammar(), name, definition)
}

// DropTable drops a table
func (d *PostgresDriver) DropTable(name string) error {
	return d.DropTableWith(d.Grammar(), name)
}

// Grammar returns the PostgreSQL query grammar
func (d *PostgresDriver) Grammar() drivers.QueryGrammar {
	return &drivers.PostgresGrammar{}
}

// DriverName returns the driver name
func (d *PostgresDriver) DriverName() string {
	return "postgres"
}

// postgresOperators registers the JSONB, full-text-search, and array overlap
// operators that the built-in scalar allowlist rejects. Postgres callers can
// chain `Where("col @> ?", json)` without falling back to Raw.
//
// JSONB ops cast the bound parameter to jsonb in the template so the user
// supplies raw JSON text and the cast lives next to the operator that needs
// it. The full-text-search `@@` op casts both sides for tsvector / tsquery
// pairing and accepts a raw text query (the cast is in the template). Array
// `&&` overlap takes a slice and renders one placeholder per element with
// ARRAY[...] bracketing.
var postgresOperators = map[string]drivers.OperatorSpec{
	"@>": {Op: "@>", Arity: 1, ParamShape: drivers.ParamJSON, Template: "{{lhs}} @> {{rhs}}::jsonb"},
	"<@": {Op: "<@", Arity: 1, ParamShape: drivers.ParamJSON, Template: "{{lhs}} <@ {{rhs}}::jsonb"},
	"?":  {Op: "?", Arity: 1, ParamShape: drivers.ParamScalar, Template: "{{lhs}} ? {{rhs}}"},
	"?|": {Op: "?|", Arity: 1, ParamShape: drivers.ParamArray, Template: "{{lhs}} ?| {{rhs}}"},
	"?&": {Op: "?&", Arity: 1, ParamShape: drivers.ParamArray, Template: "{{lhs}} ?& {{rhs}}"},
	"@@": {Op: "@@", Arity: 1, ParamShape: drivers.ParamScalar, Template: "{{lhs}} @@ to_tsquery({{rhs}})"},
	"&&": {Op: "&&", Arity: 1, ParamShape: drivers.ParamArray, Template: "{{lhs}} && {{rhs}}"},
}

// OperatorRegistry returns the postgres-specific operator extensions: JSONB
// containment / key existence, full-text search, and array overlap. Built-in
// scalar operators are unaffected; this set is consulted only when the
// allowlist misses.
func (d *PostgresDriver) OperatorRegistry() map[string]drivers.OperatorSpec {
	return postgresOperators
}
