package dbrules_test

// End-to-end outcome test for the DB-backed rules: a typed rule value built
// with validation.Unique / validation.Exists is driven through the public
// dbrules entry point against a recording *sql.DB, and the assembled query,
// bound arguments, and user-facing message are asserted. This is the seam
// where a constructor's pre-split parameters become SQL, so it pins the
// positional contract (table, column, except, id column) and the
// identifier-injection refusal.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/drivers"
	"github.com/velocitykode/velocity/validation"
	"github.com/velocitykode/velocity/validation/dbrules"
)

// --- recording database/sql plumbing -------------------------------------
//
// A minimal driver that records the last query + args handed to
// QueryRowContext and yields a single configurable count row, so the SQL each
// rule assembles is observable without a real database.

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
// Only DriverName() and DB() are exercised by the unique/exists rules. The
// remaining orm.Database methods exist only to satisfy the interface and must
// never be called.

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

func TestDBRules_TypedRulesAssembleQueries(t *testing.T) {
	cases := []struct {
		name   string
		driver string
		field  string
		rule   validation.Rule
		value  any
		count  int64
		// wantQuery is "" when the rule refuses before querying.
		wantQuery string
		wantArgs  []any
		wantMsg   string
	}{
		{
			name:      "unique taken on postgres",
			driver:    "postgres",
			field:     "email",
			rule:      validation.Unique("users", "email"),
			value:     "a@b.com",
			count:     1,
			wantQuery: `SELECT COUNT(*) FROM "users" WHERE "email" = $1`,
			wantArgs:  []any{"a@b.com"},
			wantMsg:   "The email has already been taken.",
		},
		{
			name:      "unique free on postgres",
			driver:    "postgres",
			field:     "email",
			rule:      validation.Unique("users", "email"),
			value:     "a@b.com",
			count:     0,
			wantQuery: `SELECT COUNT(*) FROM "users" WHERE "email" = $1`,
			wantArgs:  []any{"a@b.com"},
		},
		{
			name:      "unique with except and id column on mysql",
			driver:    "mysql",
			field:     "email",
			rule:      validation.Unique("users", "email").Except(5).IDColumn("user_id"),
			value:     "a@b.com",
			count:     0,
			wantQuery: "SELECT COUNT(*) FROM `users` WHERE `email` = ? AND `user_id` != ?",
			wantArgs:  []any{"a@b.com", "5"},
		},
		{
			name:      "unique excepting an app-defined id type",
			driver:    "postgres",
			field:     "email",
			rule:      validation.Unique("users", "email").Except(userID(42)),
			value:     "a@b.com",
			count:     0,
			wantQuery: `SELECT COUNT(*) FROM "users" WHERE "email" = $1 AND "id" != $2`,
			wantArgs:  []any{"a@b.com", "42"},
		},
		{
			name:      "unique with dotted identifiers on sqlite",
			driver:    "sqlite",
			field:     "email",
			rule:      validation.Unique("public.users", "users.email"),
			value:     "a@b.com",
			count:     1,
			wantQuery: "SELECT COUNT(*) FROM `public`.`users` WHERE `users`.`email` = ?",
			wantArgs:  []any{"a@b.com"},
			wantMsg:   "The email has already been taken.",
		},
		{
			name:    "unique refuses an injected table name",
			driver:  "postgres",
			field:   "email",
			rule:    validation.Unique("users; DROP TABLE users", "email"),
			value:   "x",
			count:   0,
			wantMsg: `invalid SQL identifier: "users; DROP TABLE users"`,
		},
		{
			name:      "exists present on postgres",
			driver:    "postgres",
			field:     "team_id",
			rule:      validation.Exists("teams", "id"),
			value:     7,
			count:     1,
			wantQuery: `SELECT COUNT(*) FROM "teams" WHERE "id" = $1`,
			wantArgs:  []any{int64(7)},
		},
		{
			name:      "exists missing on mysql",
			driver:    "mysql",
			field:     "team_id",
			rule:      validation.Exists("teams", "id"),
			value:     7,
			count:     0,
			wantQuery: "SELECT COUNT(*) FROM `teams` WHERE `id` = ?",
			wantArgs:  []any{int64(7)},
			wantMsg:   "The selected team_id is invalid.",
		},
		{
			name:    "exists refuses an injected column name",
			driver:  "sqlite",
			field:   "team_id",
			rule:    validation.Exists("teams", "id; --"),
			value:   7,
			count:   0,
			wantMsg: `invalid SQL identifier: "id; --"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, rec := newFakeDB(tc.driver, tc.count)

			result, err := dbrules.CheckDataWithDB(
				map[string]interface{}{tc.field: tc.value},
				validation.Rules{tc.field: {tc.rule}},
				db,
			)
			if err != nil {
				t.Fatalf("unexpected rule-set error: %v", err)
			}

			if got := result.First(tc.field); got != tc.wantMsg {
				t.Errorf("message = %q, want %q", got, tc.wantMsg)
			}
			if rec.query != tc.wantQuery {
				t.Errorf("query = %q, want %q", rec.query, tc.wantQuery)
			}
			if !reflect.DeepEqual(argValues(rec.args), tc.wantArgs) {
				t.Errorf("args = %#v, want %#v", argValues(rec.args), tc.wantArgs)
			}
		})
	}
}

// userID is a named integer type, the shape an app's typed primary key takes.
type userID int64

// TestDBRules_MissingDatabaseIsAConfigError pins that naming a DB-backed rule
// without a database wired is reported to the caller, not turned into a field
// error blaming the user's input.
func TestDBRules_MissingDatabaseIsAConfigError(t *testing.T) {
	result, err := dbrules.CheckDataWithDB(
		map[string]interface{}{"email": "a@b.com"},
		validation.Rules{"email": {validation.Required(), validation.Unique("users", "email")}},
		nil,
	)
	if err == nil {
		t.Fatal("expected an error when no database is wired")
	}
	if !errors.Is(err, validation.ErrInvalidRule) {
		t.Errorf("error does not wrap ErrInvalidRule: %v", err)
	}
	if result != nil {
		t.Error("no result should be produced when the set cannot run")
	}
}

// TestDBRules_UnregisteredRuleIsAConfigError covers a typo'd rule name on the
// DB path: the extras install unique/exists, nothing installs the typo.
func TestDBRules_UnregisteredRuleIsAConfigError(t *testing.T) {
	db, _ := newFakeDB("postgres", 0)

	_, err := dbrules.CheckDataWithDB(
		map[string]interface{}{"email": "a@b.com"},
		validation.Rules{"email": {validation.Exists("teams", "id"), typoRule{}}},
		db,
	)
	if err == nil {
		t.Fatal("expected an error for an unregistered rule name")
	}
	if !errors.Is(err, validation.ErrInvalidRule) {
		t.Errorf("error does not wrap ErrInvalidRule: %v", err)
	}
}

// typoRule names a rule that no registry, extra, or carried handler provides.
type typoRule struct{}

func (typoRule) Rule() contract.ValidationRuleSpec {
	return contract.ValidationRuleSpec{Name: "uniqe", Params: []string{"users", "email"}}
}

// argValues flattens driver.NamedValue to the bound values in ordinal order.
func argValues(in []driver.NamedValue) []any {
	if len(in) == 0 {
		return nil
	}
	out := make([]any, len(in))
	for _, a := range in {
		out[a.Ordinal-1] = a.Value
	}
	return out
}
