package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/orm/drivers"
)

// ---------------------------------------------------------------------------
// Fault-injecting database/sql driver
//
// Yields one data row, then fails on the next iteration step. database/sql
// surfaces that failure only via rows.Err() after rows.Next() returns false,
// which is exactly the path Get/Pluck/RawQuery.Get previously ignored
// (returning a truncated result set as success).
// ---------------------------------------------------------------------------

var errMidIteration = errors.New("simulated mid-iteration row failure")

func init() {
	sql.Register("orm-fault-rows", faultRowsSQLDriver{})
}

// faultRowsSQLDriver treats the DSN as a comma-separated column list for the
// rows every query returns.
type faultRowsSQLDriver struct{}

func (faultRowsSQLDriver) Open(dsn string) (driver.Conn, error) {
	return &faultRowsConn{cols: strings.Split(dsn, ",")}, nil
}

type faultRowsConn struct{ cols []string }

func (c *faultRowsConn) Prepare(string) (driver.Stmt, error) {
	return &faultRowsStmt{cols: c.cols}, nil
}
func (c *faultRowsConn) Close() error              { return nil }
func (c *faultRowsConn) Begin() (driver.Tx, error) { return nil, errors.New("tx unsupported") }

type faultRowsStmt struct{ cols []string }

func (s *faultRowsStmt) Close() error  { return nil }
func (s *faultRowsStmt) NumInput() int { return -1 }
func (s *faultRowsStmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, errors.New("exec unsupported")
}
func (s *faultRowsStmt) Query([]driver.Value) (driver.Rows, error) {
	return &faultRows{cols: s.cols}, nil
}

type faultRows struct {
	cols    []string
	yielded bool
}

func (r *faultRows) Columns() []string { return r.cols }
func (r *faultRows) Close() error      { return nil }
func (r *faultRows) Next(dest []driver.Value) error {
	if r.yielded {
		return errMidIteration
	}
	r.yielded = true
	for i, col := range r.cols {
		if col == "id" {
			dest[i] = int64(1)
		} else {
			dest[i] = "value"
		}
	}
	return nil
}

// faultDBDriver adapts the fault-injecting *sql.DB to drivers.Driver so the
// query terminals can run against it.
type faultDBDriver struct {
	nopDriver
	db *sql.DB
}

func (d *faultDBDriver) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return d.db.QueryContext(ctx, q, args...)
}

func newFaultDBDriver(t *testing.T, cols string) *faultDBDriver {
	t.Helper()
	db, err := sql.Open("orm-fault-rows", cols)
	if err != nil {
		t.Fatalf("open fault driver: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &faultDBDriver{
		nopDriver: nopDriver{grammar: &drivers.SQLiteGrammar{}},
		db:        db,
	}
}

// ---------------------------------------------------------------------------
// B24: mid-iteration failures must surface, not truncate the result set
// ---------------------------------------------------------------------------

func TestGet_RowsErrSurfaced(t *testing.T) {
	q := &Query[TestUser]{
		driver:  newFaultDBDriver(t, "id,name"),
		table:   "test_users",
		columns: []string{"*"},
	}
	results, err := q.Get(context.Background())
	if !errors.Is(err, errMidIteration) {
		t.Fatalf("Get error = %v, want %v", err, errMidIteration)
	}
	if results != nil {
		t.Errorf("Get returned partial results %v alongside the error", results)
	}
}

func TestPluck_RowsErrSurfaced(t *testing.T) {
	q := &Query[TestUser]{
		driver:  newFaultDBDriver(t, "name"),
		table:   "test_users",
		columns: []string{"*"},
	}
	results, err := q.Pluck(context.Background(), "name")
	if !errors.Is(err, errMidIteration) {
		t.Fatalf("Pluck error = %v, want %v", err, errMidIteration)
	}
	if results != nil {
		t.Errorf("Pluck returned partial results %v alongside the error", results)
	}
}

func TestRawQueryGet_RowsErrSurfaced(t *testing.T) {
	r := &RawQuery[TestUser]{
		driver: newFaultDBDriver(t, "id,name"),
		sql:    "SELECT id, name FROM test_users",
	}
	results, err := r.Get(context.Background())
	if !errors.Is(err, errMidIteration) {
		t.Fatalf("RawQuery.Get error = %v, want %v", err, errMidIteration)
	}
	if results != nil {
		t.Errorf("RawQuery.Get returned partial results %v alongside the error", results)
	}
}

// ---------------------------------------------------------------------------
// B29: Exists/DoesntExist must propagate query failures
// ---------------------------------------------------------------------------

// existsErrModel maps to a table the convenience fixture never creates, so
// every query against it fails at the database.
type existsErrModel struct {
	Model[existsErrModel]
	Name string `orm:"column:name"`
}

func (existsErrModel) TableName() string { return "exists_err_missing_table" }

func TestExists_PropagatesError(t *testing.T) {
	setupConvenienceTests(t)
	ctx := context.Background()

	exists, err := newQuery[existsErrModel]().Exists(ctx)
	if err == nil {
		t.Fatal("Query.Exists on missing table: want error, got nil")
	}
	if exists {
		t.Error("Query.Exists on missing table: exists = true, want false")
	}

	exists, err = Model[existsErrModel]{}.Exists(ctx)
	if err == nil {
		t.Fatal("Model.Exists on missing table: want error, got nil")
	}
	if exists {
		t.Error("Model.Exists on missing table: exists = true, want false")
	}

	absent, err := newQuery[existsErrModel]().DoesntExist(ctx)
	if err == nil {
		t.Fatal("Query.DoesntExist on missing table: want error, got nil")
	}
	if absent {
		t.Error("Query.DoesntExist on missing table: absent = true, want false")
	}

	absent, err = Model[existsErrModel]{}.DoesntExist(ctx)
	if err == nil {
		t.Fatal("Model.DoesntExist on missing table: want error, got nil")
	}
	if absent {
		t.Error("Model.DoesntExist on missing table: absent = true, want false")
	}
}

func TestExists_HappyPath(t *testing.T) {
	setupConvenienceTests(t)
	ctx := context.Background()

	exists, err := newQuery[TestUser]().Exists(ctx)
	if err != nil {
		t.Fatalf("Exists on empty table: unexpected error %v", err)
	}
	if exists {
		t.Error("Exists on empty table = true, want false")
	}

	seedUser(t, Default(), "Present", "present@example.com", 30)

	exists, err = newQuery[TestUser]().Exists(ctx)
	if err != nil {
		t.Fatalf("Exists on seeded table: unexpected error %v", err)
	}
	if !exists {
		t.Error("Exists on seeded table = false, want true")
	}
}

// ---------------------------------------------------------------------------
// B26: postgres placeholder renumbering in Increment/Decrement
// ---------------------------------------------------------------------------

// incCaptureDriver records the SQL and args handed to ExecContext so the
// compiled UPDATE can be asserted without a live postgres connection.
type incCaptureDriver struct {
	nopDriver
	lastSQL  string
	lastArgs []any
}

func (d *incCaptureDriver) ExecContext(_ context.Context, q string, args ...any) (sql.Result, error) {
	d.lastSQL = q
	d.lastArgs = args
	return nil, nil
}
func (d *incCaptureDriver) DriverName() string { return "postgres" }

// TestIncrement_PlaceholderRenumbering_RegressionB26 pins the renumbering of
// postgres WHERE placeholders when the amount parameter takes $1. With 11
// conditions the old descending ReplaceAll loop corrupted two-digit
// placeholders ("$11" -> "$12" -> "$22" once the i=1 pass rewrote the "$1"
// prefix); every placeholder must instead shift up by exactly one.
func TestIncrement_PlaceholderRenumbering_RegressionB26(t *testing.T) {
	d := &incCaptureDriver{nopDriver: nopDriver{grammar: &drivers.PostgresGrammar{}}}
	q := &Query[TestUser]{driver: d, table: "test_users"}
	for i := 1; i <= 11; i++ {
		q = q.Where(fmt.Sprintf("c%d = ?", i), i)
	}

	if err := q.Increment(context.Background(), "hits"); err != nil {
		t.Fatalf("Increment: %v", err)
	}

	got := regexp.MustCompile(`\$\d+`).FindAllString(d.lastSQL, -1)
	want := make([]string, 0, 12)
	for i := 1; i <= 12; i++ {
		want = append(want, fmt.Sprintf("$%d", i))
	}
	if len(got) != len(want) {
		t.Fatalf("placeholders = %v, want %v\nSQL: %s", got, want, d.lastSQL)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("placeholder %d = %s, want %s\nSQL: %s", i, got[i], want[i], d.lastSQL)
		}
	}
	if len(d.lastArgs) != 12 {
		t.Errorf("args = %d, want 12 (amount + 11 conditions)", len(d.lastArgs))
	}
}
