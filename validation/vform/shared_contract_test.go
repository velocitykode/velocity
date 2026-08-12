package vform

// The shared-contract test: router.Validatable and vform.FormRequest are one
// type, so one form struct must validate identically through ctx.BindValid
// and through vform.Form, DB-backed rules included. Both paths run against
// the same recording driver and are asserted to assemble the same query and
// reach the same verdict.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/drivers"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validation"
	"github.com/velocitykode/velocity/validation/dbrules"
)

// --- recording database/sql plumbing -------------------------------------

type queryRecorder struct {
	query string
	args  []driver.NamedValue
	count int64
}

type recConnector struct{ rec *queryRecorder }

func (c recConnector) Connect(context.Context) (driver.Conn, error) { return &recConn{rec: c.rec}, nil }
func (c recConnector) Driver() driver.Driver                        { return recDriver{} }

type recDriver struct{}

func (recDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type recConn struct{ rec *queryRecorder }

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

// recordingDB is the orm.Database the unique rule reads: only DriverName and
// DB are exercised, the rest satisfy the interface and must never be called.
type recordingDB struct {
	sqlDB *sql.DB
}

func (r *recordingDB) DriverName() string { return "postgres" }
func (r *recordingDB) DB() *sql.DB        { return r.sqlDB }

func (r *recordingDB) Raw(context.Context, string, ...any) (*sql.Rows, error)   { panic("unused") }
func (r *recordingDB) Exec(context.Context, string, ...any) (sql.Result, error) { panic("unused") }
func (r *recordingDB) Transaction(context.Context, func(context.Context) error) error {
	panic("unused")
}
func (r *recordingDB) Begin(context.Context) (*sql.Tx, error)              { panic("unused") }
func (r *recordingDB) Shutdown(context.Context) error                      { panic("unused") }
func (r *recordingDB) Ping() error                                         { panic("unused") }
func (r *recordingDB) DatabaseName() string                                { panic("unused") }
func (r *recordingDB) Stats() sql.DBStats                                  { return sql.DBStats{} }
func (r *recordingDB) DefaultDriver() drivers.Driver                       { panic("unused") }
func (r *recordingDB) Connection(string) (drivers.Driver, error)           { panic("unused") }
func (r *recordingDB) AddConnection(string, drivers.Driver)                { panic("unused") }
func (r *recordingDB) SetEventDispatcher(func(context.Context, any) error) {}

var _ orm.Database = (*recordingDB)(nil)

func newRecordingDB(count int64) (*recordingDB, *queryRecorder) {
	rec := &queryRecorder{count: count}
	return &recordingDB{sqlDB: sql.OpenDB(recConnector{rec: rec})}, rec
}

// accountRequest is one form struct used through both entry points. It
// satisfies vform.FormRequest and router.Validatable by construction: they
// are the same type.
type accountRequest struct {
	Email string `json:"email"`
}

func (accountRequest) Rules() validation.Rules {
	return validation.Rules{
		"email": {validation.Required(), validation.Email(), validation.Unique("users", "email")},
	}
}

var (
	_ FormRequest        = accountRequest{}
	_ router.Validatable = accountRequest{}
)

const wantUniqueQuery = `SELECT COUNT(*) FROM "users" WHERE "email" = $1`

// bindValidThroughRouter drives ctx.BindValid on a router wired the way
// velocity.New wires it: the data-validation seam forwards to the DB-backed
// Check helper.
func bindValidThroughRouter(t *testing.T, db orm.Database, body string) error {
	t.Helper()

	r := router.New()
	r.SetServices(&app.Services{DB: db})
	r.SetDataValidator(func(c *router.Context, data map[string]interface{}, rules contract.ValidationRuleSet, messages ...contract.ValidationMessages) error {
		result, err := dbrules.CheckDataWithDBCtx(c.Request.Context(), data, rules, db, messages...)
		if err != nil {
			return err
		}
		return result.Err()
	})

	var got error
	r.Post("/accounts", func(c *router.Context) error {
		var req accountRequest
		got = c.BindValid(&req)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, httpReq)

	return got
}

// formThroughVform drives the same struct through vform.Validate, the path
// Form[T] builds on.
func formThroughVform(t *testing.T, db orm.Database, body string) (*Result, error) {
	t.Helper()

	w := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/accounts", strings.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	ctx := router.NewContext(w, httpReq)
	ctx.SetServices(&app.Services{DB: db, Crypto: testFormEncryptor(t)})

	_, result, err := Validate[accountRequest](ctx)
	return result, err
}

func TestSharedContract_BindValidAndVformAgree(t *testing.T) {
	const body = `{"email":"a@b.com"}`

	tests := []struct {
		name     string
		count    int64
		wantFail bool
	}{
		{name: "email free", count: 0, wantFail: false},
		{name: "email taken", count: 1, wantFail: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bindDB, bindRec := newRecordingDB(tc.count)
			bindErr := bindValidThroughRouter(t, bindDB, body)

			formDB, formRec := newRecordingDB(tc.count)
			formResult, formErr := formThroughVform(t, formDB, body)
			if formErr != nil {
				t.Fatalf("vform path returned a non-validation error: %v", formErr)
			}

			// Both paths must have reached the database the same way.
			if bindRec.query != wantUniqueQuery {
				t.Errorf("BindValid query = %q, want %q", bindRec.query, wantUniqueQuery)
			}
			if bindRec.query != formRec.query {
				t.Errorf("queries diverge:\n  BindValid: %q\n  vform:     %q", bindRec.query, formRec.query)
			}
			if len(bindRec.args) != len(formRec.args) {
				t.Fatalf("arg counts diverge: BindValid %d, vform %d", len(bindRec.args), len(formRec.args))
			}
			for i := range bindRec.args {
				if bindRec.args[i].Value != formRec.args[i].Value {
					t.Errorf("arg %d diverges: BindValid %v, vform %v", i, bindRec.args[i].Value, formRec.args[i].Value)
				}
			}

			// And reached the same verdict.
			bindFailed := bindErr != nil
			formFailed := formResult != nil && formResult.HasErrors()
			if bindFailed != tc.wantFail {
				t.Errorf("BindValid failed = %v, want %v (err %v)", bindFailed, tc.wantFail, bindErr)
			}
			if formFailed != tc.wantFail {
				t.Errorf("vform failed = %v, want %v", formFailed, tc.wantFail)
			}

			if !tc.wantFail {
				return
			}

			// The same field carries the same message on both paths.
			var verr validation.ValidationErrors
			if !errors.As(bindErr, &verr) {
				t.Fatalf("BindValid error does not carry field errors: %T %v", bindErr, bindErr)
			}
			if got := verr.First("email"); got != formResult.First("email") {
				t.Errorf("messages diverge:\n  BindValid: %q\n  vform:     %q", got, formResult.First("email"))
			}
		})
	}
}

// TestSharedContract_BindValidPlainRulesThroughSeam pins that the production
// seam handles a rule set with no DB-backed rule and no database attached:
// the wiring is the one velocity.New installs, and a nil database is a
// supported state.
func TestSharedContract_BindValidPlainRulesThroughSeam(t *testing.T) {
	r := router.New()
	r.SetServices(&app.Services{})
	r.SetDataValidator(func(c *router.Context, data map[string]interface{}, rules contract.ValidationRuleSet, messages ...contract.ValidationMessages) error {
		// Mirrors velocity.New: a DB-less app hands the helper a nil
		// orm.Database rather than reaching through ctx.DB().
		result, err := dbrules.CheckDataWithDBCtx(c.Request.Context(), data, rules, nil, messages...)
		if err != nil {
			return err
		}
		return result.Err()
	})

	runPlainRules(t, r)
}

// TestSharedContract_BindValidPlainRulesWithoutSeam pins the other half of
// the contract: with no seam wired, BindValid falls back to the validator
// service and still validates an orm-free rule set.
func TestSharedContract_BindValidPlainRulesWithoutSeam(t *testing.T) {
	r := router.New()
	r.SetServices(&app.Services{Validator: validation.NewValidator()})

	runPlainRules(t, r)
}

// runPlainRules drives a passing and a failing body through BindValid on the
// supplied router.
func runPlainRules(t *testing.T, r *router.VelocityRouterV2) {
	t.Helper()

	var got error
	r.Post("/signup", func(c *router.Context) error {
		var req plainRequest
		got = c.BindValid(&req)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	for _, tc := range []struct {
		name     string
		body     string
		wantFail bool
	}{
		{name: "valid", body: `{"email":"a@b.com"}`, wantFail: false},
		{name: "invalid", body: `{"email":"nope"}`, wantFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got = nil
			rec := httptest.NewRecorder()
			httpReq := httptest.NewRequest(http.MethodPost, "/signup", strings.NewReader(tc.body))
			httpReq.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(rec, httpReq)

			if failed := got != nil; failed != tc.wantFail {
				t.Errorf("failed = %v, want %v (err %v)", failed, tc.wantFail, got)
			}
		})
	}
}

// plainRequest carries only orm-free rules.
type plainRequest struct {
	Email string `json:"email"`
}

func (plainRequest) Rules() validation.Rules {
	return validation.Rules{"email": {validation.Required(), validation.Email()}}
}
