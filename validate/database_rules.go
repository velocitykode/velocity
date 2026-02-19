package validate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/validation"
)

// identifierRegex validates SQL table/column names.
var identifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func validateIdentifier(name string) error {
	if !identifierRegex.MatchString(name) {
		return fmt.Errorf("invalid SQL identifier: %q", name)
	}
	return nil
}

// quoteIdentifier quotes a SQL identifier based on the database driver.
func quoteIdentifier(name, driver string) string {
	switch driver {
	case "mysql", "sqlite":
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	default: // postgres and others
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
}

// placeholder returns the correct parameter placeholder for the driver.
// MySQL/SQLite use ?, Postgres uses $1, $2, etc.
func placeholder(driver string, n int) string {
	if driver == "postgres" {
		return "$" + strconv.Itoa(n)
	}
	return "?"
}

// uniqueRule returns a RuleHandler that checks database uniqueness.
//
// Syntax: unique:table,column[,except_id[,id_column]]
//
//	"email": {"required", "email", "unique:users,email"}
//	"email": {"required", "email", "unique:users,email,5"}       // exclude id=5
//	"email": {"required", "email", "unique:users,email,5,user_id"} // custom id column
func uniqueRule(db *orm.Manager) validation.RuleHandler {
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
			return fmt.Errorf("unable to validate %s", field)
		}

		if count > 0 {
			return fmt.Errorf("the %s has already been taken", field)
		}
		return nil
	}
}

// existsRule returns a RuleHandler that checks a value exists in the database.
//
// Syntax: exists:table,column
//
//	"team_id": {"required", "exists:teams,id"}
func existsRule(db *orm.Manager) validation.RuleHandler {
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
			return fmt.Errorf("unable to validate %s", field)
		}

		if count == 0 {
			return fmt.Errorf("the selected %s is invalid", field)
		}
		return nil
	}
}
