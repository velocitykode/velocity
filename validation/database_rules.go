package validation

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/velocitykode/velocity/orm"
)

// identifierRegex validates SQL table/column names. The regex allows dots so
// callers can pass schema-qualified names like "public.users" or compound
// references like "users.email"; validateIdentifier then relies on
// quoteIdentifier to split on each dot and quote the parts individually so
// that "schema.table" becomes `"schema"."table"` (or "`schema`.`table`" on
// MySQL/SQLite) — not `"schema.table"`.
var identifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*$`)

func validateIdentifier(name string) error {
	if name == "" || !identifierRegex.MatchString(name) {
		return fmt.Errorf("invalid SQL identifier: %q", name)
	}
	return nil
}

// quoteIdentifier quotes a SQL identifier based on the database driver.
//
// Dotted identifiers such as "schema.table" or "table.column" are split on
// `.` and each segment is quoted independently, producing:
//
//	postgres: "schema"."table"      or "table"."column"
//	mysql:    `schema`.`table`      or `table`.`column`
//	sqlite:   `schema`.`table`      or `table`.`column`
//
// Backticks/double-quotes embedded in an individual segment are escaped by
// doubling them (standard SQL identifier-quoting rules). validateIdentifier
// refuses any character outside [a-zA-Z0-9_.] so escaping here is
// conservative — it exists to close the door on future inputs that might
// relax the allowlist.
func quoteIdentifier(name, driver string) string {
	parts := strings.Split(name, ".")
	for i, p := range parts {
		switch driver {
		case "mysql", "sqlite":
			parts[i] = "`" + strings.ReplaceAll(p, "`", "``") + "`"
		default: // postgres and other ANSI-style quoters
			parts[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
		}
	}
	return strings.Join(parts, ".")
}

// placeholder returns the correct parameter placeholder for the driver.
// MySQL/SQLite use ?, Postgres uses $1, $2, etc.
func placeholder(driver string, n int) string {
	if driver == "postgres" {
		return "$" + strconv.Itoa(n)
	}
	return "?"
}

// UniqueRule returns a RuleHandler that checks database uniqueness.
//
// Syntax: unique:table,column[,except_id[,id_column]]
//
//	"email": "required|email|unique:users,email"
//	"email": "required|email|unique:users,email,5"         // exclude id=5
//	"email": "required|email|unique:users,email,5,user_id" // custom id column
func UniqueRule(db orm.Database) RuleHandler {
	driver := db.DriverName()
	return func(field string, value interface{}, params []string, data map[string]interface{}) error {
		if len(params) < 1 {
			return fmt.Errorf("unique rule requires at least a table name")
		}

		table := params[0]
		column := field
		if len(params) >= 2 && params[1] != "" {
			column = params[1]
		}

		if err := validateIdentifier(table); err != nil {
			return err
		}
		if err := validateIdentifier(column); err != nil {
			return err
		}

		argN := 1
		query := "SELECT COUNT(*) FROM " + quoteIdentifier(table, driver) + " WHERE " + quoteIdentifier(column, driver) + " = " + placeholder(driver, argN)
		args := []interface{}{value}

		if len(params) >= 3 && params[2] != "" {
			idColumn := "id"
			if len(params) >= 4 && params[3] != "" {
				idColumn = params[3]
			}
			if err := validateIdentifier(idColumn); err != nil {
				return err
			}
			argN++
			query += " AND " + quoteIdentifier(idColumn, driver) + " != " + placeholder(driver, argN)
			args = append(args, params[2])
		}

		var count int64
		if err := db.DB().QueryRow(query, args...).Scan(&count); err != nil {
			return fmt.Errorf("Unable to validate %s.", field)
		}

		if count > 0 {
			return fmt.Errorf("The %s has already been taken.", field)
		}
		return nil
	}
}

// ExistsRule returns a RuleHandler that checks a value exists in the database.
//
// Syntax: exists:table,column
//
//	"team_id": "required|exists:teams,id"
func ExistsRule(db orm.Database) RuleHandler {
	driver := db.DriverName()
	return func(field string, value interface{}, params []string, data map[string]interface{}) error {
		if len(params) < 1 {
			return fmt.Errorf("exists rule requires at least a table name")
		}

		table := params[0]
		column := field
		if len(params) >= 2 && params[1] != "" {
			column = params[1]
		}

		if err := validateIdentifier(table); err != nil {
			return err
		}
		if err := validateIdentifier(column); err != nil {
			return err
		}

		query := "SELECT COUNT(*) FROM " + quoteIdentifier(table, driver) + " WHERE " + quoteIdentifier(column, driver) + " = " + placeholder(driver, 1)

		var count int64
		if err := db.DB().QueryRow(query, value).Scan(&count); err != nil {
			return fmt.Errorf("Unable to validate %s.", field)
		}

		if count == 0 {
			return fmt.Errorf("The selected %s is invalid.", field)
		}
		return nil
	}
}
