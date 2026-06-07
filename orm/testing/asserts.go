package testing

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/velocitykode/velocity/orm"
)

// TestingT is a subset of testing.T used by the database assertions. *testing.T
// satisfies it, and it lets the helpers' failure paths be exercised with a
// capturing fake in tests.
type TestingT interface {
	Helper()
	Errorf(format string, args ...interface{})
}

// placeholder returns the driver-specific positional placeholder for the
// argument at the given 1-indexed position. Postgres uses $1, $2, ...; mysql
// and sqlite use ?.
func placeholder(driver string, position int) string {
	if driver == "postgres" {
		return fmt.Sprintf("$%d", position)
	}
	return "?"
}

// buildCountQuery builds a parameterized SELECT COUNT(*) query against the
// given table, with a WHERE clause derived from criteria. Column keys are
// sorted for deterministic SQL. Every table and column identifier is validated
// and quoted via quoteIdentifier so no unvalidated identifier is ever
// concatenated into SQL; values are bound as parameters, never interpolated.
//
// extra is an optional already-safe predicate (e.g. "deleted_at IS NOT NULL")
// appended to the WHERE clause; its column name must be validated by the caller.
func buildCountQuery(driver, table string, criteria map[string]any, extra string) (string, []any) {
	var sb strings.Builder
	sb.WriteString("SELECT COUNT(*) FROM ")
	sb.WriteString(quoteIdentifier(table, driver))

	keys := make([]string, 0, len(criteria))
	for k := range criteria {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	args := make([]any, 0, len(keys))
	clauses := make([]string, 0, len(keys)+1)
	for _, k := range keys {
		clauses = append(clauses, quoteIdentifier(k, driver)+" = "+placeholder(driver, len(args)+1))
		args = append(args, criteria[k])
	}
	if extra != "" {
		clauses = append(clauses, extra)
	}

	if len(clauses) > 0 {
		sb.WriteString(" WHERE ")
		sb.WriteString(strings.Join(clauses, " AND "))
	}

	return sb.String(), args
}

// countRows runs a parameterized COUNT query and returns the scanned count.
// On any query/scan error it reports via t.Errorf and returns ok=false.
func countRows(t TestingT, m *orm.Manager, query string, args []any) (int, bool) {
	t.Helper()

	rows, err := m.Raw(context.Background(), query, args...)
	if err != nil {
		t.Errorf("query failed: %v\nquery: %s", err, query)
		return 0, false
	}
	defer rows.Close()

	var count int
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			t.Errorf("query failed: %v\nquery: %s", err, query)
			return 0, false
		}
		t.Errorf("count query returned no rows\nquery: %s", query)
		return 0, false
	}
	if err := rows.Scan(&count); err != nil {
		t.Errorf("failed to scan count: %v\nquery: %s", err, query)
		return 0, false
	}
	return count, true
}

// AssertDatabaseHas asserts that at least one row in table matches criteria.
func AssertDatabaseHas(t TestingT, m *orm.Manager, table string, criteria map[string]any) {
	t.Helper()

	driver := m.DriverName()
	query, args := buildCountQuery(driver, table, criteria, "")
	count, ok := countRows(t, m, query, args)
	if !ok {
		return
	}
	if count == 0 {
		t.Errorf("AssertDatabaseHas: expected at least one row in %q matching %v, found %d", table, criteria, count)
	}
}

// AssertDatabaseMissing asserts that no row in table matches criteria.
func AssertDatabaseMissing(t TestingT, m *orm.Manager, table string, criteria map[string]any) {
	t.Helper()

	driver := m.DriverName()
	query, args := buildCountQuery(driver, table, criteria, "")
	count, ok := countRows(t, m, query, args)
	if !ok {
		return
	}
	if count != 0 {
		t.Errorf("AssertDatabaseMissing: expected no rows in %q matching %v, found %d", table, criteria, count)
	}
}

// AssertDatabaseCount asserts that table contains exactly expected rows.
func AssertDatabaseCount(t TestingT, m *orm.Manager, table string, expected int) {
	t.Helper()

	driver := m.DriverName()
	query, args := buildCountQuery(driver, table, nil, "")
	count, ok := countRows(t, m, query, args)
	if !ok {
		return
	}
	if count != expected {
		t.Errorf("AssertDatabaseCount: expected %d rows in %q, found %d", expected, table, count)
	}
}

// AssertSoftDeleted asserts that at least one soft-deleted row (deleted_at IS
// NOT NULL) in table matches criteria.
func AssertSoftDeleted(t TestingT, m *orm.Manager, table string, criteria map[string]any) {
	t.Helper()

	driver := m.DriverName()
	predicate := quoteIdentifier("deleted_at", driver) + " IS NOT NULL"
	query, args := buildCountQuery(driver, table, criteria, predicate)
	count, ok := countRows(t, m, query, args)
	if !ok {
		return
	}
	if count == 0 {
		t.Errorf("AssertSoftDeleted: expected at least one soft-deleted row in %q matching %v, found %d", table, criteria, count)
	}
}

// AssertNotSoftDeleted asserts that at least one non-soft-deleted row
// (deleted_at IS NULL) in table matches criteria.
func AssertNotSoftDeleted(t TestingT, m *orm.Manager, table string, criteria map[string]any) {
	t.Helper()

	driver := m.DriverName()
	predicate := quoteIdentifier("deleted_at", driver) + " IS NULL"
	query, args := buildCountQuery(driver, table, criteria, predicate)
	count, ok := countRows(t, m, query, args)
	if !ok {
		return
	}
	if count == 0 {
		t.Errorf("AssertNotSoftDeleted: expected at least one non-soft-deleted row in %q matching %v, found %d", table, criteria, count)
	}
}
