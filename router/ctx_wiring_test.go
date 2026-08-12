package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// TestTimeoutClonePropagatesFullWiring verifies the Context clone built
// by Timeout carries the complete router wiring. Before the ctxWiring
// unification the clone was populated field-by-field and had drifted:
// fileRoot and intendedFn were omitted, so under Timeout(...) ctx.File/
// Download/SaveFile failed with "no file root configured" and
// ctx.Intended always fell back.
func TestTimeoutClonePropagatesFullWiring(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New()
	r.SetFileRoot(dir)
	r.SetIntendedResolver(func(c *Context) string { return "/from-session" })
	r.SetValidator(func(c *Context, rules contract.ValidationRuleSet, messages ...contract.ValidationMessages) error {
		return nil
	})
	r.SetDataValidator(func(c *Context, data map[string]interface{}, rules contract.ValidationRuleSet, messages ...contract.ValidationMessages) error {
		return nil
	})
	r.Use(Timeout(time.Second))

	var sawFileRoot, sawIntendedFn, sawValidateFn, sawValidateDataFn bool
	var intended string
	r.Get("/probe", func(c *Context) error {
		sawFileRoot = c.fileRoot != nil
		sawIntendedFn = c.intendedFn != nil
		sawValidateFn = c.validateFn != nil
		sawValidateDataFn = c.validateDataFn != nil
		intended = c.Intended("/fallback")
		return c.File("hello.txt")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/probe", nil))

	if !sawFileRoot {
		t.Error("Timeout clone missing fileRoot")
	}
	if !sawIntendedFn {
		t.Error("Timeout clone missing intendedFn")
	}
	if !sawValidateFn {
		t.Error("Timeout clone missing validateFn")
	}
	if !sawValidateDataFn {
		t.Error("Timeout clone missing validateDataFn")
	}
	if intended != "/from-session" {
		t.Errorf("Intended under Timeout: expected /from-session, got %q", intended)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("File under Timeout: expected 200, got %d (body %q)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "hi" {
		t.Errorf("File under Timeout: expected body %q, got %q", "hi", rec.Body.String())
	}
}

// TestNotFoundContextGetsFullWiring verifies contexts on the
// handleNotFound path receive the same wiring as matched routes, by
// recording wiring presence from a global middleware on an unknown path.
func TestNotFoundContextGetsFullWiring(t *testing.T) {
	dir := t.TempDir()

	r := New()
	r.SetFileRoot(dir)
	r.SetIntendedResolver(func(c *Context) string { return "" })
	r.SetValidator(func(c *Context, rules contract.ValidationRuleSet, messages ...contract.ValidationMessages) error {
		return nil
	})
	r.SetDataValidator(func(c *Context, data map[string]interface{}, rules contract.ValidationRuleSet, messages ...contract.ValidationMessages) error {
		return nil
	})

	var sawFileRoot, sawIntendedFn, sawValidateFn, sawValidateDataFn bool
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			sawFileRoot = c.fileRoot != nil
			sawIntendedFn = c.intendedFn != nil
			sawValidateFn = c.validateFn != nil
			sawValidateDataFn = c.validateDataFn != nil
			return next(c)
		}
	})
	r.Get("/ok", func(c *Context) error { return nil })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/no-such-path", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if !sawFileRoot {
		t.Error("not-found context missing fileRoot")
	}
	if !sawIntendedFn {
		t.Error("not-found context missing intendedFn")
	}
	if !sawValidateDataFn {
		t.Error("not-found context missing validateDataFn")
	}
	if !sawValidateFn {
		t.Error("not-found context missing validateFn")
	}
}
