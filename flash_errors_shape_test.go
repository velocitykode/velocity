package velocity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/router"
	vhttp "github.com/velocitykode/velocity/testing/http"
	"github.com/velocitykode/velocity/validation"
	"github.com/velocitykode/velocity/validation/vform"
	"github.com/velocitykode/velocity/view"
)

// flashShapeLogin is the starter login form: the first thing the starter
// login handler does is validate it through vform.
type flashShapeLogin struct {
	Email    string `json:"email" form:"email"`
	Password string `json:"password" form:"password"`
}

func (flashShapeLogin) Rules() validation.Rules {
	return validation.Rules{
		"email":    {validation.Required(), validation.Email()},
		"password": {validation.Required(), validation.Min(8)},
	}
}

// flashShapeApp boots a real app (New + Bootstrap) the way a starter does
// (ConfigFromEnv, AUTH_SCHEME=web) with the view engine's Inertia
// middleware, a login page and the POST routes whose flashed errors
// the page renders. It returns the app, a cookie-carrying client that does
// not follow redirects, and the test server.
func flashShapeApp(t *testing.T) (*App, *http.Client, *httptest.Server) {
	t.Helper()
	for k, v := range map[string]string{
		"APP_ENV":        "local",
		"APP_KEY":        strings.Repeat("k", 32),
		"AUTH_SCHEME":    "web",
		"LOG_DRIVER":     "null",
		"CACHE_DRIVER":   "memory",
		"QUEUE_DRIVER":   "memory",
		"MAIL_DRIVER":    "log",
		"SESSION_SECURE": "false", // plain-HTTP httptest server
	} {
		t.Setenv(k, v)
	}
	cfg := ConfigFromEnv()
	cfg.View = view.Config{RootTemplate: inertiaTestTemplate, Version: "v1"}
	a, err := New(WithConfig(cfg))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	a.Router.Use(a.Services.View.(*view.Engine).Middleware())

	// A validation failure through vform.
	a.Router.Post("/vform", func(c *router.Context) error {
		_, err := vform.Form[flashShapeLogin](c)
		return err
	})
	// The same failure returned under a named error bag.
	a.Router.Post("/vform-bag", func(c *router.Context) error {
		_, err := vform.Form[flashShapeLogin](c)
		var failure *validation.Failure
		if errors.As(err, &failure) {
			failure.Bag = "login"
			return failure
		}
		return err
	})
	// Handler-flashed errors, as the starter login handler flashes a
	// credentials mismatch.
	a.Router.Post("/handler", func(c *router.Context) error {
		c.FlashErrors(map[string][]string{"email": {"These credentials do not match our records."}})
		view.Back(c)
		return nil
	})
	a.Router.Post("/handler-strings", func(c *router.Context) error {
		c.FlashErrors(map[string]string{"email": "These credentials do not match our records."})
		view.Back(c)
		return nil
	})
	a.Router.Get("/login", func(c *router.Context) error {
		return view.For(c).Render("Auth/Login", view.Props{})
	})

	srv := httptest.NewServer(a.Router)
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return a, client, srv
}

// loginPageErrors posts the bad login form to path, then renders the login
// page as an Inertia visit and returns its errors prop.
func loginPageErrors(t *testing.T, client *http.Client, base, path string) map[string]any {
	t.Helper()
	form := url.Values{"email": {"bad"}, "password": {"x"}}
	req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("new POST %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", base+"/login")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
		t.Fatalf("POST %s = %d, want a redirect back", path, resp.StatusCode)
	}

	req, err = http.NewRequest(http.MethodGet, base+"/login", nil)
	if err != nil {
		t.Fatalf("new GET /login: %v", err)
	}
	req.Header.Set("X-Inertia", "true")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Inertia-Version", "v1")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var page struct {
		Props map[string]any `json:"props"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode login page after POST %s: %v", path, err)
	}
	errs, ok := page.Props["errors"].(map[string]any)
	if !ok {
		t.Fatalf("login page after POST %s: errors prop = %#v, want an object", path, page.Props["errors"])
	}
	return errs
}

// TestFlashedErrors_OneShape asserts every source of flashed errors gives
// the page the same shape: errors.<field> is the field's first message as
// a string, whether the errors came from a vform failure, a bagged failure
// or a handler flashing a map. A bagged failure also exposes its fields
// under errors.<bag>.
func TestFlashedErrors_OneShape(t *testing.T) {
	_, client, srv := flashShapeApp(t)
	tests := []struct {
		path      string
		wantEmail string
		bag       string
	}{
		{path: "/vform", wantEmail: "The email field must be a valid email address."},
		{path: "/vform-bag", wantEmail: "The email field must be a valid email address.", bag: "login"},
		{path: "/handler", wantEmail: "These credentials do not match our records."},
		{path: "/handler-strings", wantEmail: "These credentials do not match our records."},
	}
	for _, tt := range tests {
		t.Run(strings.TrimPrefix(tt.path, "/"), func(t *testing.T) {
			errs := loginPageErrors(t, client, srv.URL, tt.path)
			if got, ok := errs["email"].(string); !ok || got != tt.wantEmail {
				t.Errorf("errors.email = %#v (%T), want the string %q", errs["email"], errs["email"], tt.wantEmail)
			}
			if tt.bag == "" {
				return
			}
			bagged, ok := errs[tt.bag].(map[string]any)
			if !ok {
				t.Fatalf("errors.%s = %#v, want an object", tt.bag, errs[tt.bag])
			}
			if got, ok := bagged["email"].(string); !ok || got != tt.wantEmail {
				t.Errorf("errors.%s.email = %#v (%T), want the string %q", tt.bag, bagged["email"], bagged["email"], tt.wantEmail)
			}
		})
	}
}

// flashShapeRecorder records assertion failures from the testing/http
// helpers so a test can assert that an assertion passed.
type flashShapeRecorder struct{ failures []string }

func (*flashShapeRecorder) Helper() {}

func (r *flashShapeRecorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

// TestAssertSessionHasErrors_EverySource asserts the testing/http helper
// finds a flashed field whatever produced it, including a failure flashed
// under a named error bag.
func TestAssertSessionHasErrors_EverySource(t *testing.T) {
	a, _, _ := flashShapeApp(t)
	scheme, err := auth.FromServices(a.Services).DefaultScheme()
	if err != nil {
		t.Fatalf("DefaultScheme: %v", err)
	}
	sessionScheme, ok := scheme.(*schemes.SessionScheme)
	if !ok {
		t.Fatalf("default scheme is %T, want *schemes.SessionScheme", scheme)
	}
	form := url.Values{"email": {"bad"}, "password": {"x"}}
	for _, path := range []string{"/vform", "/vform-bag", "/handler", "/handler-strings"} {
		t.Run(strings.TrimPrefix(path, "/"), func(t *testing.T) {
			rec := &flashShapeRecorder{}
			vhttp.NewTestClient(rec, a.Router).
				WithHeader("Referer", "/login").
				PostForm(path, form).
				AssertSessionHasErrors(sessionScheme, "email")
			if len(rec.failures) != 0 {
				t.Errorf("AssertSessionHasErrors(scheme, \"email\") after POST %s failed: %v", path, rec.failures)
			}
		})
	}
}
