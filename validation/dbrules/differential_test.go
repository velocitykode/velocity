package dbrules_test

// Differential parity test: the DEPRECATED reflection shims in the core
// validation package and the typed dbrules surface now share one
// implementation (validation/internal/dbcheck). This test feeds IDENTICAL
// inputs through BOTH public surfaces against a recording *sql.DB and asserts
// they assemble byte-identical queries + args and return identical error
// strings, so the two surfaces can never silently diverge.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/drivers"
	"github.com/velocitykode/velocity/validation"
	"github.com/velocitykode/velocity/validation/dbrules"
)

// --- recording database/sql plumbing -------------------------------------
//
// A minimal driver that records the last query + args handed to
// QueryRowContext and yields a single configurable count row, so the SQL each
// surface assembles is observable without a real database.

type recorder struct {
	query string
	args  []driver.NamedValue
	count int64
}

type recConnector struct{ rec *recorder }

func (c recConnector) Connect(context.Context) (driver.Conn, error) { return &recConn{rec: c.rec}, nil }
func (c recConnector) Driver() driver.Driver                        { return recDriver{} }

type recDriver struct{}

func (recDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type recConn struct{ rec *recorder }

func (c *recConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not used") }
func (c *recConn) Close() error                        { return nil }
func (c *recConn) Begin() (driver.Tx, error)           { return nil, errors.New("not used") }

func (c *recConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.rec.query = query
	c.rec.args = args
	return &recRows{count: c.rec.count}, nil
}

type recRows struct {
	count int64
	done  bool
}

func (r *recRows) Columns() []string { return []string{"count"} }
func (r *recRows) Close() error      { return nil }
func (r *recRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.count
	return nil
}

// --- fake orm.Database wrapping the recording *sql.DB --------------------
//
// Only DriverName() and DB() are exercised by the unique/exists rules (the
// typed surface reads them directly; the reflection shim reaches them via
// MethodByName). The remaining orm.Database methods exist only to satisfy the
// interface and must never be called.

type fakeDB struct {
	driver string
	sqlDB  *sql.DB
}

func (f *fakeDB) DriverName() string { return f.driver }
func (f *fakeDB) DB() *sql.DB        { return f.sqlDB }

func (f *fakeDB) Raw(context.Context, string, ...any) (*sql.Rows, error) {
	panic("unused")
}
func (f *fakeDB) Exec(context.Context, string, ...any) (sql.Result, error)       { panic("unused") }
func (f *fakeDB) Transaction(context.Context, func(context.Context) error) error { panic("unused") }
func (f *fakeDB) Begin(context.Context) (*sql.Tx, error)                         { panic("unused") }
func (f *fakeDB) Shutdown(context.Context) error                                 { panic("unused") }
func (f *fakeDB) Ping() error                                                    { panic("unused") }
func (f *fakeDB) DatabaseName() string                                           { panic("unused") }
func (f *fakeDB) Stats() sql.DBStats                                             { panic("unused") }
func (f *fakeDB) DefaultDriver() drivers.Driver                                  { panic("unused") }
func (f *fakeDB) Connection(string) (drivers.Driver, error)                      { panic("unused") }
func (f *fakeDB) AddConnection(string, drivers.Driver)                           { panic("unused") }
func (f *fakeDB) SetEventDispatcher(func(context.Context, any) error)            { panic("unused") }

var _ orm.Database = (*fakeDB)(nil)

func newFakeDB(driverName string, count int64) (*fakeDB, *recorder) {
	rec := &recorder{count: count}
	return &fakeDB{driver: driverName, sqlDB: sql.OpenDB(recConnector{rec: rec})}, rec
}

// surfaces returns the unique/exists rule handler from each surface for the
// same db, so the test can drive both with identical (field, value, params).
// The compat shim takes db as `any` (reflection seam); dbrules takes the typed
// orm.Database. Both must produce the same handler behavior.
type surface struct {
	unique validation.RuleHandler
	exists validation.RuleHandler
}

func TestDifferential_CompatVsDbrules(t *testing.T) {
	cases := []struct {
		name   string
		driver string
		rule   string // "unique" or "exists"
		field  string
		value  any
		params []string
		count  int64
		// wantErrSubstr is "" when the rule should pass (nil error).
		wantErrSubstr string
	}{
		{"unique-taken-pg", "postgres", "unique", "email", "a@b.com", []string{"users", "email"}, 1, "already been taken"},
		{"unique-free-pg", "postgres", "unique", "email", "a@b.com", []string{"users", "email"}, 0, ""},
		{"unique-exceptid-mysql", "mysql", "unique", "email", "a@b.com", []string{"users", "email", "5", "user_id"}, 0, ""},
		{"unique-dotted-sqlite", "sqlite", "unique", "email", "a@b.com", []string{"public.users", "users.email"}, 1, "already been taken"},
		{"unique-bad-ident", "postgres", "unique", "email", "x", []string{"users; DROP TABLE users", "email"}, 0, "invalid SQL identifier"},
		{"exists-present-pg", "postgres", "exists", "team_id", 7, []string{"teams", "id"}, 1, ""},
		{"exists-missing-mysql", "mysql", "exists", "team_id", 7, []string{"teams", "id"}, 0, "is invalid"},
		{"exists-bad-ident", "sqlite", "exists", "team_id", 7, []string{"teams", "id; --"}, 0, "invalid SQL identifier"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			compatDB, compatRec := newFakeDB(tc.driver, tc.count)
			dbrulesDB, dbrulesRec := newFakeDB(tc.driver, tc.count)

			compat := surface{
				unique: validation.UniqueRule(compatDB),
				exists: validation.ExistsRule(compatDB),
			}
			typed := surface{
				unique: dbrules.UniqueRule(dbrulesDB),
				exists: dbrules.ExistsRule(dbrulesDB),
			}

			run := func(s surface) error {
				if tc.rule == "unique" {
					return s.unique(tc.field, tc.value, tc.params, nil)
				}
				return s.exists(tc.field, tc.value, tc.params, nil)
			}

			compatErr := run(compat)
			typedErr := run(typed)

			// Error strings must match between surfaces.
			if errStr(compatErr) != errStr(typedErr) {
				t.Fatalf("error mismatch: compat=%q dbrules=%q", errStr(compatErr), errStr(typedErr))
			}
			// And match the expected substring (or nil).
			if tc.wantErrSubstr == "" {
				if compatErr != nil {
					t.Fatalf("expected nil error, got %v", compatErr)
				}
			} else if compatErr == nil || !strings.Contains(compatErr.Error(), tc.wantErrSubstr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErrSubstr, compatErr)
			}

			// When the identifier is rejected the query never runs, so the
			// recorders stay empty; both surfaces must agree on that too.
			if compatRec.query != dbrulesRec.query {
				t.Fatalf("query mismatch:\n  compat:  %q\n  dbrules: %q", compatRec.query, dbrulesRec.query)
			}
			if !reflect.DeepEqual(namedArgs(compatRec.args), namedArgs(dbrulesRec.args)) {
				t.Fatalf("args mismatch:\n  compat:  %v\n  dbrules: %v", compatRec.args, dbrulesRec.args)
			}
		})
	}
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// namedArgs flattens driver.NamedValue to comparable (ordinal,value) pairs;
// the ordinal positions and bound values must be identical across surfaces.
func namedArgs(in []driver.NamedValue) []any {
	out := make([]any, 0, len(in))
	for _, a := range in {
		out = append(out, [2]any{a.Ordinal, a.Value})
	}
	return out
}
