package velocity

// The validation callbacks New() installs on the router are exercised here
// through a real app with NO database configured, which initDB reports by
// returning a nil manager. Both callbacks must validate orm-free rules
// normally and report a DB-backed rule as a configuration error, never
// panic: c.DB() is fatal when the service is unset, so neither callback may
// route through it.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validation"
)

// dblessApp builds a real app with no DB_CONNECTION, the configuration
// NewTestApp produces, and asserts the database really is absent.
func dblessApp(t *testing.T) *App {
	t.Helper()
	v, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(func() { _ = v.Shutdown(t.Context()) })
	if v.DB != nil {
		t.Fatalf("expected a DB-less app, got %T", v.DB)
	}
	return v
}

// plainRules and dbRules are the two rule sets the callbacks must tell apart.
func plainRules() validation.Rules {
	return validation.Rules{"email": {validation.Required(), validation.Email()}}
}

func dbBackedRules() validation.Rules {
	return validation.Rules{"email": {validation.Required(), validation.Unique("users", "email")}}
}

// postJSON drives one request through the app's router.
func postJSON(t *testing.T, v *App, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	v.Router.ServeHTTP(rec, req)
	return rec
}

func TestValidateCallback_WithoutDatabase(t *testing.T) {
	v := dblessApp(t)

	var got error
	v.Router.Post("/plain", func(c *router.Context) error {
		got = c.Validate(plainRules())
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	v.Router.Post("/db", func(c *router.Context) error {
		got = c.Validate(dbBackedRules())
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	t.Run("plain rules pass", func(t *testing.T) {
		got = nil
		postJSON(t, v, "/plain", `{"email":"a@b.com"}`)
		if got != nil {
			t.Fatalf("ctx.Validate error = %v, want nil", got)
		}
	})

	t.Run("plain rules fail", func(t *testing.T) {
		got = nil
		postJSON(t, v, "/plain", `{"email":"nope"}`)
		if !errors.Is(got, router.ErrValidationAborted) {
			t.Fatalf("ctx.Validate error = %v, want ErrValidationAborted", got)
		}
	})

	t.Run("db rule is a config error", func(t *testing.T) {
		got = nil
		postJSON(t, v, "/db", `{"email":"a@b.com"}`)
		if got == nil {
			t.Fatal("expected an error for a DB rule with no database")
		}
		if !errors.Is(got, validation.ErrInvalidRule) {
			t.Fatalf("error does not wrap ErrInvalidRule: %v", got)
		}
		if errors.Is(got, router.ErrValidationAborted) {
			t.Error("a missing database must not read as a field failure")
		}
	})
}

// bindValidRequest carries orm-free rules only.
type bindValidRequest struct {
	Email string `json:"email"`
}

func (bindValidRequest) Rules() validation.Rules { return plainRules() }

// bindValidDBRequest names a DB-backed rule.
type bindValidDBRequest struct {
	Email string `json:"email"`
}

func (bindValidDBRequest) Rules() validation.Rules { return dbBackedRules() }

func TestDataValidatorCallback_WithoutDatabase(t *testing.T) {
	v := dblessApp(t)

	var got error
	v.Router.Post("/bind-plain", func(c *router.Context) error {
		var req bindValidRequest
		got = c.BindValid(&req)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	v.Router.Post("/bind-db", func(c *router.Context) error {
		var req bindValidDBRequest
		got = c.BindValid(&req)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	t.Run("plain rules pass", func(t *testing.T) {
		got = nil
		postJSON(t, v, "/bind-plain", `{"email":"a@b.com"}`)
		if got != nil {
			t.Fatalf("BindValid error = %v, want nil", got)
		}
	})

	t.Run("plain rules fail", func(t *testing.T) {
		got = nil
		postJSON(t, v, "/bind-plain", `{"email":"nope"}`)
		if got == nil {
			t.Fatal("expected a validation error")
		}
		var verr validation.ValidationErrors
		if !errors.As(got, &verr) {
			t.Fatalf("BindValid error does not carry field errors: %T %v", got, got)
		}
		if verr.First("email") == "" {
			t.Error("expected a message on the email field")
		}
	})

	t.Run("db rule is a config error", func(t *testing.T) {
		got = nil
		postJSON(t, v, "/bind-db", `{"email":"a@b.com"}`)
		if got == nil {
			t.Fatal("expected an error for a DB rule with no database")
		}
		if !errors.Is(got, validation.ErrInvalidRule) {
			t.Fatalf("error does not wrap ErrInvalidRule: %v", got)
		}
		var verr validation.ValidationErrors
		if errors.As(got, &verr) {
			t.Error("a missing database must not read as a field failure")
		}
	})
}
