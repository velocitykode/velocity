package velocity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/view"
)

// TestInertiaErrorPage_FlashDrainedByErrorPageStaysDrained drives a flash
// through a real app with the cookie session store: request 1 flashes a
// message; request 2 fails with a 404 whose Inertia error page drains the
// flash through the session flash bag, and the session change the error
// page made must reach the browser on that 404; request 3 then carries no
// flash.
func TestInertiaErrorPage_FlashDrainedByErrorPageStaysDrained(t *testing.T) {
	enc, err := crypto.NewEncryptor(crypto.Config{Key: strings.Repeat("k", 32), Cipher: "AES-256-GCM"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	sessionScheme, err := schemes.NewSessionScheme(&countingUserStore{}, auth.SessionConfig{
		Name:         "vel_session",
		IdleLifetime: 60,
		Path:         "/",
		HttpOnly:     true,
		SameSite:     http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}

	a, err := New(WithConfig(Config{
		Env:   "testing",
		Port:  "0",
		Cache: CacheConfig{Driver: "memory", Prefix: "test_cache"},
		Log:   log.LogConfig{Driver: "null", Config: make(map[string]any)},
		Queue: QueueConfig{Driver: "memory"},
		Mail:  mail.MailConfig{Driver: "log"},
		View:  view.Config{RootTemplate: inertiaTestTemplate, Version: "v1", ErrorPage: "Error"},
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	a.Errors(func(h contract.ErrorHandler) { h.SetDebug(false) })
	m := auth.FromServices(a.Services)
	if m == nil {
		t.Fatal("app has no *auth.Manager")
	}
	m.RegisterScheme("web", sessionScheme)
	// Bootstrap installs the session middleware for the session scheme.
	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	engine, ok := a.Services.View.(*view.Engine)
	if !ok {
		t.Fatal("view engine not built")
	}
	a.Router.Use(engine.Middleware())
	a.Router.Get("/flash", func(c *router.Context) error {
		m.Session(c.Request).Flash("notice", "saved")
		return c.String(http.StatusOK, "ok")
	})
	a.Router.Get("/missing", func(*router.Context) error { return problem.NotFound() })

	srv := httptest.NewServer(a.Router)
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{Jar: jar}

	resp, err := client.Get(srv.URL + "/flash")
	if err != nil {
		t.Fatalf("GET /flash: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /flash = %d, want 200", resp.StatusCode)
	}

	missing := func() (*http.Response, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/missing", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("X-Inertia", "true")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("X-Inertia-Version", "v1")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET /missing: %v", err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET /missing = %d, want 404 (body %s)", resp.StatusCode, raw)
		}
		var page map[string]any
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatalf("decode page: %v (%s)", err, raw)
		}
		if page["component"] != "Error" {
			t.Fatalf("component = %v, want Error", page["component"])
		}
		return resp, page
	}

	resp2, page2 := missing()
	flash, _ := page2["flash"].(map[string]any)
	if flash["notice"] != "saved" {
		t.Errorf("request 2 flash = %v, want notice=saved", page2["flash"])
	}
	sessionCookie := false
	for _, c := range resp2.Cookies() {
		if c.Name == "vel_session" {
			sessionCookie = true
		}
	}
	if !sessionCookie {
		t.Error("the 404 carries no vel_session cookie: the drained flash was not saved")
	}

	_, page3 := missing()
	if f, ok := page3["flash"]; ok {
		t.Errorf("request 3 flash = %v, want none (the flash showed again)", f)
	}
}

// TestInertiaErrorPage_FlashDrainStaysDrainedWhereverBondIsRegistered
// drives the flash round trip of
// TestInertiaErrorPage_FlashDrainedByErrorPageStaysDrained with the bond
// middleware added through Router.Use after Bootstrap and before it (as
// the Inertia docs show). Bootstrap puts the session middleware at the
// front of the global list either way, so bond's response buffer never
// sits outside it and hides the response writer's pre-commit hook.
//
// Request 1 flashes notice=saved. Request 2 is an Inertia GET that returns
// problem.NotFound(); its error page shows the flash once and drains it
// through the session flash bag, so that 404 must carry the updated session
// cookie. Requests 3 and 4 (the same 404) must then carry no flash.
func TestInertiaErrorPage_FlashDrainStaysDrainedWhereverBondIsRegistered(t *testing.T) {
	tests := []struct {
		name       string
		bondBefore bool // engine.Middleware() is registered before Bootstrap
	}{
		{name: "BondRegisteredAfterBootstrap", bondBefore: false},
		{name: "BondRegisteredBeforeBootstrap", bondBefore: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc, err := crypto.NewEncryptor(crypto.Config{Key: strings.Repeat("k", 32), Cipher: "AES-256-GCM"})
			if err != nil {
				t.Fatalf("NewEncryptor: %v", err)
			}
			sessionScheme, err := schemes.NewSessionScheme(&countingUserStore{}, auth.SessionConfig{
				Name:         "vel_session",
				IdleLifetime: 60,
				Path:         "/",
				HttpOnly:     true,
				SameSite:     http.SameSiteLaxMode,
			}, enc)
			if err != nil {
				t.Fatalf("NewSessionScheme: %v", err)
			}

			a, err := New(WithConfig(Config{
				Env:   "testing",
				Port:  "0",
				Cache: CacheConfig{Driver: "memory", Prefix: "test_cache"},
				Log:   log.LogConfig{Driver: "null", Config: make(map[string]any)},
				Queue: QueueConfig{Driver: "memory"},
				Mail:  mail.MailConfig{Driver: "log"},
				View:  view.Config{RootTemplate: inertiaTestTemplate, Version: "v1", ErrorPage: "Error"},
			}))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
			a.Errors(func(h contract.ErrorHandler) { h.SetDebug(false) })
			m := auth.FromServices(a.Services)
			if m == nil {
				t.Fatal("app has no *auth.Manager")
			}
			m.RegisterScheme("web", sessionScheme)

			engine, ok := a.Services.View.(*view.Engine)
			if !ok {
				t.Fatal("view engine not built")
			}

			if tt.bondBefore {
				a.Router.Use(engine.Middleware())
			}
			// Bootstrap installs the session middleware for the session scheme.
			if err := a.Bootstrap(); err != nil {
				t.Fatalf("Bootstrap: %v", err)
			}
			if !tt.bondBefore {
				a.Router.Use(engine.Middleware())
			}
			a.Router.Get("/flash", func(c *router.Context) error {
				m.Session(c.Request).Flash("notice", "saved")
				return c.String(http.StatusOK, "ok")
			})
			a.Router.Get("/missing", func(*router.Context) error { return problem.NotFound() })

			srv := httptest.NewServer(a.Router)
			t.Cleanup(srv.Close)
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatalf("cookiejar: %v", err)
			}
			client := &http.Client{Jar: jar}

			resp, err := client.Get(srv.URL + "/flash")
			if err != nil {
				t.Fatalf("GET /flash: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET /flash = %d, want 200", resp.StatusCode)
			}

			missing := func() (*http.Response, map[string]any) {
				t.Helper()
				req, err := http.NewRequest(http.MethodGet, srv.URL+"/missing", nil)
				if err != nil {
					t.Fatalf("NewRequest: %v", err)
				}
				req.Header.Set("X-Inertia", "true")
				req.Header.Set("X-Requested-With", "XMLHttpRequest")
				req.Header.Set("X-Inertia-Version", "v1")
				resp, err := client.Do(req)
				if err != nil {
					t.Fatalf("GET /missing: %v", err)
				}
				defer resp.Body.Close()
				raw, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				if resp.StatusCode != http.StatusNotFound {
					t.Fatalf("GET /missing = %d, want 404 (body %s)", resp.StatusCode, raw)
				}
				var page map[string]any
				if err := json.Unmarshal(raw, &page); err != nil {
					t.Fatalf("decode page: %v (%s)", err, raw)
				}
				if page["component"] != "Error" {
					t.Fatalf("component = %v, want Error", page["component"])
				}
				return resp, page
			}

			// Request 2: the error page shows the flash once and drains it.
			resp2, page2 := missing()
			flash, _ := page2["flash"].(map[string]any)
			if flash["notice"] != "saved" {
				t.Fatalf("request 2 flash = %v, want notice=saved (the error page shows the flash once)", page2["flash"])
			}
			sessionCookie := false
			for _, c := range resp2.Cookies() {
				if c.Name == "vel_session" {
					sessionCookie = true
				}
			}
			if !sessionCookie {
				t.Errorf("request 2: the 404 whose error page drained the flash carries no vel_session "+
					"Set-Cookie (bondBefore=%v): the drain was never saved", tt.bondBefore)
			}

			// Requests 3 and 4: the drained flash must stay drained.
			for i := 3; i <= 4; i++ {
				_, page := missing()
				if f, ok := page["flash"]; ok {
					t.Errorf("request %d flash = %v, want none (bondBefore=%v): the error page replayed "+
						"a flash an earlier error page already drained", i, f, tt.bondBefore)
				}
			}
		})
	}
}
