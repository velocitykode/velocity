// Package mysql is the per-driver leaf that backs Velocity's ORM with MySQL
// via go-sql-driver/mysql. It lives outside orm/drivers so the ORM core never
// pulls in the mysql driver: importing this package (directly or via
// orm/standard) registers the "mysql" driver factory and attaches the driver
// only to the binaries that ask for it.
//
//	import _ "github.com/velocitykode/velocity/orm/mysql"
//
// The driver self-registers into orm.Drivers() from init(); use New for a
// connected driver without going through the registry.
package mysql

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	_ "github.com/go-sql-driver/mysql"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/drivers"
)

// init registers the mysql driver into the canonical ORM registry. The driver
// lives in this leaf package so the orm root never pulls in the mysql driver;
// importing this package (directly or via orm/standard) wires the "mysql"
// factory.
func init() {
	orm.Drivers().Register("mysql", func(_ context.Context, cfg drivers.ConnectionConfig) (drivers.Driver, error) {
		return New(cfg)
	})
}

// New constructs a connected MySQL driver from cfg for standalone use without
// going through the ORM driver registry. It returns the same driver the
// registry path produces, so both routes are equivalent.
func New(cfg drivers.ConnectionConfig) (drivers.Driver, error) {
	d := &MySQLDriver{}
	if err := d.Connect(cfg); err != nil {
		return nil, err
	}
	return d, nil
}

// mysqlDSNPasswordRegex matches the password portion of a MySQL DSN between
// the first ':' after the optional username and the '@' that starts the host
// section. Password characters are URL-encoded so the capture ends at '@'.
var mysqlDSNPasswordRegex = regexp.MustCompile(`^([^:@/]+):[^@]*@`)

// redactMySQLDSN masks the password portion of a MySQL DSN so it can be
// embedded in error messages and logs without leaking credentials.
func redactMySQLDSN(dsn string) string {
	return mysqlDSNPasswordRegex.ReplaceAllString(dsn, `$1:[REDACTED]@`)
}

// resolveMySQLTLS returns the effective tls= value for a MySQL connection.
//
// Precedence:
//  1. Config.TLS, if set. Applications may pass "true", "skip-verify",
//     "preferred", or a named TLS profile registered via
//     github.com/go-sql-driver/mysql RegisterTLSConfig.
//  2. "preferred", the secure default. Uses TLS if the server offers it,
//     otherwise falls back to plaintext.
func resolveMySQLTLS(configured string) string {
	if configured != "" {
		return configured
	}
	return "preferred"
}

// MySQLDriver implements the drivers.Driver interface for MySQL.
type MySQLDriver struct {
	drivers.BaseDriver
}

// NewMySQLDriver creates a new (unconnected) MySQL driver instance.
func NewMySQLDriver() drivers.Driver {
	return &MySQLDriver{}
}

// Connect establishes a connection to a MySQL database.
func (d *MySQLDriver) Connect(config drivers.ConnectionConfig) error {
	d.Config = config

	// Build DSN: user:password@tcp(host:port)/dbname
	// URL-encode username and password to handle special characters
	escapedUser := url.QueryEscape(config.Username)
	var dsn string
	if config.Password != "" {
		escapedPass := url.QueryEscape(config.Password)
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s",
			escapedUser,
			escapedPass,
			config.Host,
			config.Port,
			config.Database,
		)
	} else {
		dsn = fmt.Sprintf("%s@tcp(%s:%s)/%s",
			escapedUser,
			config.Host,
			config.Port,
			config.Database,
		)
	}

	// Add parameters
	params := []string{"parseTime=true"}
	if config.Charset != "" {
		params = append(params, "charset="+config.Charset)
	} else {
		params = append(params, "charset=utf8mb4")
	}
	if config.Collation != "" {
		params = append(params, "collation="+config.Collation)
	}
	if config.TimeZone != "" {
		params = append(params, "loc="+config.TimeZone)
	}
	// tls defaults to preferred, secure by default.
	params = append(params, "tls="+resolveMySQLTLS(config.TLS))

	dsn += "?" + strings.Join(params, "&")

	if err := d.OpenAndPing("mysql", dsn); err != nil {
		return fmt.Errorf("velocity/orm: mysql connect failed (dsn=%q): %w", redactMySQLDSN(dsn), err)
	}
	return nil
}

// HasTable checks if a table exists
func (d *MySQLDriver) HasTable(name string) bool {
	sql := d.Grammar().CompileHasTable(name)
	var tableName string
	err := d.DB().QueryRow(sql, name).Scan(&tableName)
	return err == nil && tableName == name
}

// HasColumn checks if a column exists in a table
func (d *MySQLDriver) HasColumn(table, column string) bool {
	sql := d.Grammar().CompileHasColumn(table, column)
	var count int
	err := d.DB().QueryRow(sql, d.Config.Database, table, column).Scan(&count)
	return err == nil && count > 0
}

// CreateTable creates a new table
func (d *MySQLDriver) CreateTable(name string, definition func(*drivers.Table)) error {
	return d.CreateTableWith(d.Grammar(), name, definition)
}

// DropTable drops a table
func (d *MySQLDriver) DropTable(name string) error {
	return d.DropTableWith(d.Grammar(), name)
}

// Grammar returns the MySQL query grammar
func (d *MySQLDriver) Grammar() drivers.QueryGrammar {
	return &drivers.MySQLGrammar{}
}

// DriverName returns the driver name
func (d *MySQLDriver) DriverName() string {
	return "mysql"
}

// OperatorRegistry returns nil. MySQL gains no extension operators in this
// release; the seam is in place for JSON_CONTAINS / JSON_OVERLAPS follow-ups.
func (d *MySQLDriver) OperatorRegistry() map[string]drivers.OperatorSpec {
	return nil
}
