package velocity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/maintpath"
	"github.com/velocitykode/velocity/router"
)

// useTempMaintRoot points the maintenance path resolver at a brand new tmp
// directory for the duration of the test. The resolver caches its result on
// first call, so the cache is also reset.
func useTempMaintRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(maintpath.EnvVar, root)
	maintpath.Reset()
	t.Cleanup(maintpath.Reset)
	// Also reset the one-time WARN log gate so each test exercises the
	// resolution log path identically (otherwise gate state would leak).
	maintenancePathLogOnce = sync.Once{}
	return root
}

// createMarker creates the .vel/down marker file under the resolver's
// currently-configured root and returns its absolute path.
func createMarker(t *testing.T, content string) string {
	t.Helper()
	root, err := maintpath.Root()
	if err != nil {
		t.Fatalf("resolve maint root: %v", err)
	}
	dir := filepath.Join(root, maintpath.MarkerDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("failed to create %s dir: %v", maintpath.MarkerDir, err)
	}
	markerPath := filepath.Join(dir, maintpath.MarkerFile)
	if err := os.WriteFile(markerPath, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write marker: %v", err)
	}
	return markerPath
}

func TestPreventRequestsDuringMaintenance_AppIsUp(t *testing.T) {
	useTempMaintRoot(t)

	mw := PreventRequestsDuringMaintenance()
	handler := mw(func(c *router.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	c, w := router.NewTestContext("GET", "/")
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}
}

func TestPreventRequestsDuringMaintenance_AppIsDown(t *testing.T) {
	useTempMaintRoot(t)
	createMarker(t, `{"time":"2024-01-01T00:00:00Z"}`)

	mw := PreventRequestsDuringMaintenance()
	nextCalled := false
	handler := mw(func(c *router.Context) error {
		nextCalled = true
		return nil
	})

	c, w := router.NewTestContext("GET", "/")
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if nextCalled {
		t.Error("next handler should not be called during maintenance")
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body["message"] != "Service Unavailable" {
		t.Errorf("message: got %q, want %q", body["message"], "Service Unavailable")
	}
}

func TestPreventRequestsDuringMaintenance_RecoversAfterUp(t *testing.T) {
	useTempMaintRoot(t)
	markerPath := createMarker(t, `{}`)

	mw := PreventRequestsDuringMaintenance()
	handler := mw(func(c *router.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// Request while down — should get 503.
	c1, w1 := router.NewTestContext("GET", "/")
	if err := handler(c1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w1.Code != http.StatusServiceUnavailable {
		t.Fatalf("while down: got %d, want %d", w1.Code, http.StatusServiceUnavailable)
	}

	// Remove marker (simulate "up" command).
	os.Remove(markerPath)

	// Request after up — should pass through.
	c2, w2 := router.NewTestContext("GET", "/")
	if err := handler(c2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w2.Code != http.StatusOK {
		t.Errorf("after up: got %d, want %d", w2.Code, http.StatusOK)
	}
}

func TestPreventRequestsDuringMaintenance_ContentType(t *testing.T) {
	useTempMaintRoot(t)
	createMarker(t, `{}`)

	mw := PreventRequestsDuringMaintenance()
	handler := mw(func(c *router.Context) error { return nil })

	c, w := router.NewTestContext("GET", "/")
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}
}

func TestPreventRequestsDuringMaintenance_Concurrent(t *testing.T) {
	useTempMaintRoot(t)
	createMarker(t, `{}`)

	mw := PreventRequestsDuringMaintenance()
	handler := mw(func(c *router.Context) error {
		return c.JSON(http.StatusOK, nil)
	})

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, w := router.NewTestContext("GET", "/")
			if err := handler(c); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("concurrent: got %d, want %d", w.Code, http.StatusServiceUnavailable)
			}
		}()
	}
	wg.Wait()
}

func TestIsDownForMaintenance(t *testing.T) {
	useTempMaintRoot(t)

	if isDownForMaintenance() {
		t.Error("should return false when marker does not exist")
	}

	createMarker(t, `{}`)

	if !isDownForMaintenance() {
		t.Error("should return true when marker exists")
	}
}

// TestBypass_SecretPathMintsCookieAndRedirects asserts that hitting "/" + secret
// while in maintenance mode mints a bypass cookie and 302-redirects to "/".
func TestBypass_SecretPathMintsCookieAndRedirects(t *testing.T) {
	useTempMaintRoot(t)
	t.Setenv("APP_ENV", "testing")
	createMarker(t, `{"secret":"letmein","time":"2026-01-01T00:00:00Z"}`)

	mw := PreventRequestsDuringMaintenance()
	nextCalled := false
	handler := mw(func(c *router.Context) error {
		nextCalled = true
		return nil
	})

	c, w := router.NewTestContext("GET", "/letmein")
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if w.Code != http.StatusFound {
		t.Fatalf("status: got %d, want %d", w.Code, http.StatusFound)
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Errorf("Location: got %q, want %q", loc, "/")
	}
	if nextCalled {
		t.Error("next handler should not be invoked on the mint path")
	}

	cookies := w.Result().Cookies()
	var bypass *http.Cookie
	for _, ck := range cookies {
		if ck.Name == maintenanceBypassCookie {
			bypass = ck
			break
		}
	}
	if bypass == nil {
		t.Fatal("expected bypass cookie to be set")
	}
	if !bypass.HttpOnly {
		t.Error("bypass cookie must be HttpOnly")
	}
	if bypass.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite: got %v, want Lax", bypass.SameSite)
	}
	if bypass.Path != "/" {
		t.Errorf("Path: got %q, want %q", bypass.Path, "/")
	}
}

// TestBypass_ValidCookieSkipsMaintenance asserts that a request carrying a
// freshly minted bypass cookie passes through to the inner handler.
func TestBypass_ValidCookieSkipsMaintenance(t *testing.T) {
	useTempMaintRoot(t)
	t.Setenv("APP_ENV", "testing")
	createMarker(t, `{"secret":"letmein"}`)

	cookie := mintMaintenanceBypassCookie("letmein", maintenanceBypassDefaultTTL)

	mw := PreventRequestsDuringMaintenance()
	nextCalled := false
	handler := mw(func(c *router.Context) error {
		nextCalled = true
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	c, w := router.NewTestContext("GET", "/dashboard")
	c.Request.AddCookie(cookie)
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !nextCalled {
		t.Error("expected handler to be invoked when bypass cookie is valid")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}
}

// TestBypass_TamperedCookieIgnored asserts that any modification to the cookie
// value (truncate, flip a byte, swap MAC) is rejected and the request still
// receives a 503.
func TestBypass_TamperedCookieIgnored(t *testing.T) {
	useTempMaintRoot(t)
	t.Setenv("APP_ENV", "testing")
	createMarker(t, `{"secret":"letmein"}`)

	good := mintMaintenanceBypassCookie("letmein", maintenanceBypassDefaultTTL)

	cases := []struct {
		name  string
		value string
	}{
		{"truncated", good.Value[:len(good.Value)-4]},
		{"flipped-byte", flipFirstByte(good.Value)},
		{"junk", "!!!not-base64!!!"},
		{"empty", ""},
		{"missing-mac", base64.RawURLEncoding.EncodeToString([]byte("9999999999"))},
		{"bad-expiry", base64.RawURLEncoding.EncodeToString([]byte("notanumber:deadbeef"))},
	}

	mw := PreventRequestsDuringMaintenance()
	handler := mw(func(c *router.Context) error {
		return c.JSON(http.StatusOK, nil)
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := router.NewTestContext("GET", "/dashboard")
			c.Request.AddCookie(&http.Cookie{Name: maintenanceBypassCookie, Value: tc.value})
			if err := handler(c); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("status: got %d, want 503 for tampered cookie", w.Code)
			}
		})
	}
}

// TestBypass_ExpiredCookieIgnored asserts that a syntactically valid cookie
// whose expires_at is in the past is rejected.
func TestBypass_ExpiredCookieIgnored(t *testing.T) {
	useTempMaintRoot(t)
	t.Setenv("APP_ENV", "testing")
	createMarker(t, `{"secret":"letmein"}`)

	// Mint with negative TTL so the embedded expiry is already in the past.
	expired := mintMaintenanceBypassCookie("letmein", -time.Hour)

	mw := PreventRequestsDuringMaintenance()
	handler := mw(func(c *router.Context) error {
		return c.JSON(http.StatusOK, nil)
	})

	c, w := router.NewTestContext("GET", "/dashboard")
	c.Request.AddCookie(expired)
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503 for expired cookie", w.Code)
	}
}

// TestBypass_AbsentCookieReturns503 asserts the baseline: no bypass cookie,
// in maintenance, request returns 503.
func TestBypass_AbsentCookieReturns503(t *testing.T) {
	useTempMaintRoot(t)
	t.Setenv("APP_ENV", "testing")
	createMarker(t, `{"secret":"letmein"}`)

	mw := PreventRequestsDuringMaintenance()
	handler := mw(func(c *router.Context) error {
		return c.JSON(http.StatusOK, nil)
	})

	c, w := router.NewTestContext("GET", "/dashboard")
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503", w.Code)
	}
}

// TestBypass_NoSecretInDownFileDisablesBypass asserts that when the down-file
// has no secret, even a syntactically valid cookie (signed under a guessed
// secret) cannot bypass maintenance, and that hitting an arbitrary path does
// not mint a cookie.
func TestBypass_NoSecretInDownFileDisablesBypass(t *testing.T) {
	useTempMaintRoot(t)
	t.Setenv("APP_ENV", "testing")
	createMarker(t, `{"time":"2026-01-01T00:00:00Z"}`)

	// A cookie minted for some other secret should not be honoured.
	cookie := mintMaintenanceBypassCookie("guess", maintenanceBypassDefaultTTL)

	mw := PreventRequestsDuringMaintenance()
	handler := mw(func(c *router.Context) error {
		return c.JSON(http.StatusOK, nil)
	})

	// Bypass-by-cookie disabled.
	c1, w1 := router.NewTestContext("GET", "/dashboard")
	c1.Request.AddCookie(cookie)
	if err := handler(c1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w1.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503 when no secret configured", w1.Code)
	}

	// Mint-by-path disabled: hitting "/guess" (or any path) does not mint.
	c2, w2 := router.NewTestContext("GET", "/guess")
	if err := handler(c2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w2.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503 when no secret configured", w2.Code)
	}
	for _, ck := range w2.Result().Cookies() {
		if ck.Name == maintenanceBypassCookie {
			t.Errorf("bypass cookie minted when no secret configured: %q", ck.Value)
		}
	}
}

// TestBypass_WrongSecretCookieIgnored asserts that a cookie minted under a
// different secret cannot bypass when the down-file holds a real secret.
// Catches cross-environment cookie replay between staging and prod.
func TestBypass_WrongSecretCookieIgnored(t *testing.T) {
	useTempMaintRoot(t)
	t.Setenv("APP_ENV", "testing")
	createMarker(t, `{"secret":"prod-secret"}`)

	cookie := mintMaintenanceBypassCookie("staging-secret", maintenanceBypassDefaultTTL)

	mw := PreventRequestsDuringMaintenance()
	handler := mw(func(c *router.Context) error {
		return c.JSON(http.StatusOK, nil)
	})

	c, w := router.NewTestContext("GET", "/")
	c.Request.AddCookie(cookie)
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503 for wrong-secret cookie", w.Code)
	}
}

// TestBypass_MalformedDownFileBlocksBypass asserts that an unparseable
// down-file still triggers maintenance mode and disables bypass entirely.
func TestBypass_MalformedDownFileBlocksBypass(t *testing.T) {
	useTempMaintRoot(t)
	t.Setenv("APP_ENV", "testing")
	createMarker(t, `{not valid json`)

	cookie := mintMaintenanceBypassCookie("any", maintenanceBypassDefaultTTL)

	mw := PreventRequestsDuringMaintenance()
	handler := mw(func(c *router.Context) error {
		return c.JSON(http.StatusOK, nil)
	})

	c, w := router.NewTestContext("GET", "/any", nil)
	c.Request.AddCookie(cookie)
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503 for malformed down-file", w.Code)
	}
}

// TestShouldUseSecureBypassCookie covers the APP_ENV-driven Secure flag.
func TestShouldUseSecureBypassCookie(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"", true},
		{"production", true},
		{"staging", true},
		{"development", false},
		{"dev", false},
		{"testing", false},
		{"test", false},
		{"DEVELOPMENT", false},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.env)
			if got := shouldUseSecureBypassCookie(); got != tc.want {
				t.Errorf("env=%q: got %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

// TestDeriveMaintenanceMACKey_RejectsEmptySecret asserts the derivation
// helper rejects an empty secret rather than producing a deterministic key.
func TestDeriveMaintenanceMACKey_RejectsEmptySecret(t *testing.T) {
	if _, err := deriveMaintenanceMACKey(""); err == nil {
		t.Error("expected error for empty secret")
	}
}

// TestDeriveMaintenanceMACKey_AppKeyBindsKey asserts that flipping APP_KEY
// yields a different derived key for the same secret, so a cookie minted
// under one APP_KEY does not verify under another.
func TestDeriveMaintenanceMACKey_AppKeyBindsKey(t *testing.T) {
	t.Setenv("APP_KEY", "key-one")
	k1, err := deriveMaintenanceMACKey("secret")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}

	t.Setenv("APP_KEY", "key-two")
	k2, err := deriveMaintenanceMACKey("secret")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}

	if bytes.Equal(k1, k2) {
		t.Error("expected APP_KEY to affect derived MAC key")
	}
}

// flipFirstByte returns s with its first character replaced by a distinct
// character so the resulting string is the same length but byte-different.
func flipFirstByte(s string) string {
	if s == "" {
		return "X"
	}
	first := s[0]
	repl := byte('A')
	if first == repl {
		repl = 'B'
	}
	return string(repl) + s[1:]
}

// TestMaintenance_PathSurvivesCWDDrift exercises the M-39 contract: the
// resolved marker path is bound to the configured root, not the current
// working directory, so a process launched in (or that chdirs to) a
// different dir still finds the marker.
func TestMaintenance_PathSurvivesCWDDrift(t *testing.T) {
	root := useTempMaintRoot(t)
	createMarker(t, `{}`)

	// Chdir somewhere completely unrelated. If the resolver were still
	// cwd-relative the marker would appear absent here and the request
	// would slip through with 200 instead of 503.
	t.Chdir("/tmp")

	mw := PreventRequestsDuringMaintenance()
	handler := mw(func(c *router.Context) error {
		return c.JSON(http.StatusOK, nil)
	})

	c, w := router.NewTestContext("GET", "/")
	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503 (root=%s)", w.Code, root)
	}
}

// TestMaintenance_RejectsInvalidEnvRoot exercises CLAUDE.md rule 4: an
// operator-supplied path with a `..` segment must be rejected and must NOT
// silently fall through to maintenance-on. Invalid env means the middleware
// treats it as "no marker file present" so a typo cannot lock the app.
func TestMaintenance_RejectsInvalidEnvRoot(t *testing.T) {
	// NUL is rejected at the os.Setenv layer by the runtime so it cannot
	// be exercised through t.Setenv; the maintpath package-level test
	// covers it directly via validateEnvRoot.
	cases := []struct {
		name string
		val  string
	}{
		{"relative", "rel/path"},
		{"dotdot", "/var/../etc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(maintpath.EnvVar, tc.val)
			maintpath.Reset()
			t.Cleanup(maintpath.Reset)
			maintenancePathLogOnce = sync.Once{}

			mw := PreventRequestsDuringMaintenance()
			handler := mw(func(c *router.Context) error {
				return c.JSON(http.StatusOK, nil)
			})

			c, w := router.NewTestContext("GET", "/")
			if err := handler(c); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if w.Code != http.StatusOK {
				t.Errorf("status: got %d, want 200 (invalid env means no marker)", w.Code)
			}
		})
	}
}
