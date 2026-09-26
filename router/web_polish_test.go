package router

import (
	"bytes"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
)

// --- QueryFloat64 parse-error handling ---

// QueryFloat64 must mirror QueryInt64: on a parse error with no default
// it returns 0, never ParseFloat's partial result. Range errors are the
// regression case: ParseFloat("1e999", 64) returns +Inf AND an error,
// and the old code returned that +Inf to the caller.
func TestQueryFloat64_ParseErrorReturnsZeroWithoutDefault(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"syntax error", "/test?f=abc"},
		{"range error overflows to Inf", "/test?f=1e999"},
		{"negative range error", "/test?f=-1e999"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewTestContext("GET", tt.query)
			got := c.QueryFloat64("f")
			if got != 0 {
				t.Errorf("expected 0, got %v", got)
			}
			if math.IsInf(got, 0) {
				t.Errorf("parse error leaked Inf to caller: %v", got)
			}
		})
	}
}

func TestQueryFloat64_ParseErrorUsesDefault(t *testing.T) {
	c, _ := NewTestContext("GET", "/test?f=1e999")
	if got := c.QueryFloat64("f", 2.5); got != 2.5 {
		t.Errorf("expected default 2.5, got %v", got)
	}
}

func TestQueryFloat64_ValidValue(t *testing.T) {
	c, _ := NewTestContext("GET", "/test?f=3.25")
	if got := c.QueryFloat64("f", 9.9); got != 3.25 {
		t.Errorf("expected 3.25, got %v", got)
	}
}

// --- Wrap error mapping ---

// Wrap must honor *contract.HTTPError like the router's default error
// path: 4xx echoes the handler-supplied message (client-facing by
// design), 5xx and non-HTTPError errors get a generic body.
func TestWrap_HTTPError4xx_EchoesCodeAndMessage(t *testing.T) {
	h := Wrap(func(c *Context) error {
		return contract.NewHTTPError(http.StatusUnprocessableEntity, "name is required")
	})

	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/test", nil))

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) != "name is required" {
		t.Errorf("expected 4xx message echoed, got %q", w.Body.String())
	}
}

func TestWrap_WrappedHTTPError_Honored(t *testing.T) {
	h := Wrap(func(c *Context) error {
		return fmt.Errorf("lookup failed: %w", contract.NewHTTPError(http.StatusNotFound, "no such user"))
	})

	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/test", nil))

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 from wrapped HTTPError, got %d", w.Code)
	}
}

func TestWrap_HTTPError5xx_GenericBody(t *testing.T) {
	h := Wrap(func(c *Context) error {
		return contract.NewHTTPError(http.StatusServiceUnavailable, "db password rotation in flight")
	})

	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/test", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "db password rotation") {
		t.Errorf("5xx body leaked server-side detail: %q", body)
	}
	if strings.TrimSpace(body) != http.StatusText(http.StatusServiceUnavailable) {
		t.Errorf("expected generic status text, got %q", body)
	}
}

// --- Post-freeze registration on a retained group warns ---

// Registration through a retained group object after the first request
// must warn like top-level registration does. The routes still never
// serve (frozen tree); the warning makes that loud instead of silent.
func TestGroupRegistrationAfterFreeze_WarnsAndDoesNotServe(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	r := NewV2()
	g := r.Group("/api")
	g.Get("/before", func(c *Context) error { return c.String(http.StatusOK, "before") })

	// Freeze the router with a first request.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/before", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("pre-freeze route: expected 200, got %d", w.Code)
	}

	t.Run("route", func(t *testing.T) {
		buf.Reset()
		g.Get("/late", func(c *Context) error { return c.String(http.StatusOK, "late") })
		if !strings.Contains(buf.String(), "after server start") {
			t.Errorf("expected post-freeze warning, log: %q", buf.String())
		}

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/late", nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("post-freeze route must not serve: expected 404, got %d", w.Code)
		}
	})

	t.Run("group", func(t *testing.T) {
		buf.Reset()
		g.Group("/sub")
		if !strings.Contains(buf.String(), "after server start") {
			t.Errorf("expected post-freeze warning, log: %q", buf.String())
		}
	})

	t.Run("middleware", func(t *testing.T) {
		buf.Reset()
		g.Use(func(next HandlerFunc) HandlerFunc { return next })
		if !strings.Contains(buf.String(), "after server start") {
			t.Errorf("expected post-freeze warning, log: %q", buf.String())
		}
	})

	t.Run("resource", func(t *testing.T) {
		buf.Reset()
		g.Resource("/things", NewTestUserController())
		if !strings.Contains(buf.String(), "after server start") {
			t.Errorf("expected post-freeze warning, log: %q", buf.String())
		}
	})
}

// --- Cookie attribute unification ---

func TestDeleteCookie_SecureAndSameSiteByDefault(t *testing.T) {
	c, w := NewTestContext("GET", "/")
	c.DeleteCookie("session")

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	got := cookies[0]
	if got.MaxAge != -1 || got.Value != "" {
		t.Errorf("expected expired empty cookie, got %#v", got)
	}
	if !got.Secure {
		t.Error("deletion cookie must be Secure by default")
	}
	if got.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected SameSite=Lax, got %v", got.SameSite)
	}
	if !got.HttpOnly || got.Path != "/" {
		t.Errorf("expected HttpOnly Path=/, got %#v", got)
	}
}

func TestDeleteCookie_InsecureOptOutFollowsServices(t *testing.T) {
	c, w := NewTestContext("GET", "/")
	c.SetServices(&app.Services{CookiePolicy: contract.NewCookiePolicy("/", "", false, http.SameSiteLaxMode)})
	c.DeleteCookie("session")

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	if cookies[0].Secure {
		t.Error("expected Secure=false under an insecure cookie policy")
	}
}
