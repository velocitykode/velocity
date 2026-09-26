package velocity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/view"
)

// sessionFlashServer boots a real app the way a starter template does
// (ConfigFromEnv, AUTH_SCHEME=web, a view engine and its Inertia
// middleware), bootstrapped or in embed mode (New only), with no flash
// wiring of its own. Its routes flash a message, validation errors and old
// input, and render the page they land on.
func sessionFlashServer(t *testing.T, bootstrap bool) (*App, *httptest.Server, *http.Client) {
	t.Helper()
	for k, v := range map[string]string{
		"APP_ENV":          "local",
		"APP_KEY":          strings.Repeat("k", 32),
		"AUTH_SCHEME":      "web",
		"LOG_DRIVER":       "null",
		"CACHE_DRIVER":     "memory",
		"QUEUE_DRIVER":     "memory",
		"MAIL_DRIVER":      "log",
		"SESSION_SECURE":   "false",
		"SESSION_LIFETIME": "120",
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
	if bootstrap {
		if err := a.Bootstrap(); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
	}
	a.Router.Use(a.Services.View.(*view.Engine).Middleware())

	a.Router.Post("/save", func(c *router.Context) error {
		view.For(c).Flash("success", "Saved!").Redirect("/dash")
		return nil
	})
	a.Router.Post("/invalid", func(c *router.Context) error {
		c.FlashErrors(map[string][]string{"email": {"The email is taken."}})
		c.FlashInput(map[string]any{"email": "taken@example.test"})
		view.For(c).Flash("warning", "Check the form.").Redirect("/dash")
		return nil
	})
	a.Router.Get("/dash", func(c *router.Context) error {
		return view.For(c).Render("Dash", view.Props{"other": "x"})
	})

	srv := httptest.NewServer(a.Router)
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return a, srv, client
}

// sessionFlashPost posts an empty form to path and returns the response
// (body drained). It fails the test unless the answer is a redirect.
func sessionFlashPost(t *testing.T, client *http.Client, base, path string) *http.Response {
	t.Helper()
	resp, err := client.PostForm(base+path, url.Values{})
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
		t.Fatalf("POST %s = %d, want a redirect", path, resp.StatusCode)
	}
	return resp
}

// sessionFlashVisit makes an Inertia visit to /dash, partial (only the
// "other" prop) or full, and returns the response and the decoded page.
func sessionFlashVisit(t *testing.T, client *http.Client, base string, partial bool) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/dash", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Inertia", "true")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Inertia-Version", "v1")
	if partial {
		req.Header.Set("X-Inertia-Partial-Component", "Dash")
		req.Header.Set("X-Inertia-Partial-Data", "other")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /dash: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var page map[string]any
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode page: %v (%s)", err, raw)
	}
	return resp, page
}

// sessionCookieFlash decrypts the session cookie resp sets and returns its
// flash bag, and whether resp set the session cookie at all.
func sessionCookieFlash(t *testing.T, a *App, resp *http.Response) (map[string]any, bool) {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name != a.config.Session.Name {
			continue
		}
		plaintext, err := a.Services.Crypto.Decrypt(c.Value)
		if err != nil {
			t.Fatalf("decrypt session cookie: %v", err)
		}
		var wire struct {
			Flash map[string]any `json:"flash"`
		}
		if err := json.Unmarshal([]byte(plaintext), &wire); err != nil {
			t.Fatalf("decode session cookie: %v", err)
		}
		return wire.Flash, true
	}
	return nil, false
}

// assertOnlySessionCookie fails when resp sets any cookie other than the
// session cookie: flash data rides in the session cookie only.
func assertOnlySessionCookie(t *testing.T, a *App, step string, resp *http.Response) {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name != a.config.Session.Name {
			t.Errorf("%s: sets cookie %q; only the session cookie %q may carry flash data", step, c.Name, a.config.Session.Name)
		}
	}
}

// TestSessionFlash_ReachesTheNextPageOnce asserts a message flashed by
// view.For(ctx).Flash reaches the next Inertia page as flash.success with
// no app wiring, is gone from the page after that, and is gone from the
// session cookie once delivered.
func TestSessionFlash_ReachesTheNextPageOnce(t *testing.T) {
	for _, mode := range []struct {
		name      string
		bootstrap bool
	}{{"bootstrapped", true}, {"embed_mode", false}} {
		t.Run(mode.name, func(t *testing.T) {
			a, srv, client := sessionFlashServer(t, mode.bootstrap)

			resp := sessionFlashPost(t, client, srv.URL, "/save")
			assertOnlySessionCookie(t, a, "POST /save", resp)

			resp, page := sessionFlashVisit(t, client, srv.URL, false)
			flash, _ := page["flash"].(map[string]any)
			if flash["success"] != "Saved!" {
				t.Fatalf("first page after the redirect: flash = %#v, want success = \"Saved!\"", page["flash"])
			}
			assertOnlySessionCookie(t, a, "first GET /dash", resp)
			bag, set := sessionCookieFlash(t, a, resp)
			if !set {
				t.Fatalf("first GET /dash drained the flash but did not save the session")
			}
			if len(bag) != 0 {
				t.Errorf("session cookie after the drain still carries flash %v", bag)
			}

			_, page = sessionFlashVisit(t, client, srv.URL, false)
			if f, ok := page["flash"]; ok {
				t.Errorf("second page: flash = %#v, want none", f)
			}
		})
	}
}

// TestSessionFlash_PartialReloadDeliversErrors asserts a partial reload
// after a validation redirect delivers the flashed errors and old input
// even though its only list does not name them, and leaves the message
// flash for the next full render, so nothing is consumed undelivered.
func TestSessionFlash_PartialReloadDeliversErrors(t *testing.T) {
	a, srv, client := sessionFlashServer(t, true)

	resp := sessionFlashPost(t, client, srv.URL, "/invalid")
	assertOnlySessionCookie(t, a, "POST /invalid", resp)

	resp, page := sessionFlashVisit(t, client, srv.URL, true)
	assertOnlySessionCookie(t, a, "partial GET /dash", resp)
	props, _ := page["props"].(map[string]any)
	errs, _ := props["errors"].(map[string]any)
	if errs["email"] != "The email is taken." {
		t.Errorf("partial reload: errors = %#v, want email = \"The email is taken.\"", props["errors"])
	}
	old, _ := props["old"].(map[string]any)
	if old["email"] != "taken@example.test" {
		t.Errorf("partial reload: old = %#v, want email = \"taken@example.test\"", props["old"])
	}
	if f, ok := page["flash"]; ok {
		t.Errorf("partial reload: flash = %#v, want it left for the next full render", f)
	}

	_, page = sessionFlashVisit(t, client, srv.URL, false)
	flash, _ := page["flash"].(map[string]any)
	if flash["warning"] != "Check the form." {
		t.Errorf("full render after the partial reload: flash = %#v, want warning = \"Check the form.\"", page["flash"])
	}
	props, _ = page["props"].(map[string]any)
	if e, ok := props["errors"]; ok {
		t.Errorf("full render after the partial reload: errors = %#v, want them delivered once", e)
	}
}
