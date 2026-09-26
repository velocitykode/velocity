package csrf

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/csrf/stores"
)

// tokenLifetimeFixture is a CSRF middleware on one live session over the
// session-less memory store with a short idle lifetime.
func tokenLifetimeFixture(t *testing.T, idle time.Duration) (http.Handler, string) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Store = stores.NewMemoryStore(idle)
	cfg.CookiePolicy = contract.NewCookiePolicy("/", "", false, http.SameSiteLaxMode)
	cfg.SessionIDResolver = func(*http.Request) (string, error) { return "live-session", nil }
	c, err := NewE(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Shutdown(t.Context()) })
	h := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	// Page load: the GET mints the token and writes XSRF-TOKEN.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/form", nil))
	for _, ck := range w.Result().Cookies() {
		if ck.Name == "XSRF-TOKEN" {
			if ck.MaxAge != 0 || !ck.Expires.IsZero() {
				t.Fatalf("XSRF-TOKEN carries a clock of its own: MaxAge=%d Expires=%v, want a browser-session cookie", ck.MaxAge, ck.Expires)
			}
			tok, _ := url.QueryUnescape(ck.Value)
			return h, tok
		}
	}
	t.Fatal("no XSRF-TOKEN written")
	return nil, ""
}

func postWithPageToken(h http.Handler, tok string) int {
	r := httptest.NewRequest(http.MethodPost, "/save", nil)
	r.Header.Set("X-CSRF-Token", tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

// The memory store keeps an active session's token: every validation
// restarts the token's idle clock, so a page's token is accepted long past
// the idle lifetime as long as requests keep coming.
func TestMemoryStoreToken_ActiveSessionNeverAgesOut(t *testing.T) {
	h, tok := tokenLifetimeFixture(t, 300*time.Millisecond)
	start := time.Now()
	for time.Since(start) < time.Second {
		if code := postWithPageToken(h, tok); code != http.StatusOK {
			t.Fatalf("POST with the page token on an active session got %d at %v (idle lifetime 300ms)", code, time.Since(start).Round(10*time.Millisecond))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// An idle token in the memory store still expires: no request for longer
// than the idle lifetime and the old page's token is refused.
func TestMemoryStoreToken_IdleTokenExpires(t *testing.T) {
	h, tok := tokenLifetimeFixture(t, 100*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	if code := postWithPageToken(h, tok); code != 419 {
		t.Fatalf("POST with a token idle past its lifetime got %d, want %d", code, 419)
	}
}
