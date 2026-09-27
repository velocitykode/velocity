package csrf

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/velocitykode/velocity/csrf/stores"
)

// xsrfCookieValues returns the unescaped values of every XSRF-TOKEN
// Set-Cookie line on w, in header order.
func xsrfCookieValues(t *testing.T, h http.Header) []string {
	t.Helper()
	var out []string
	for _, line := range h.Values("Set-Cookie") {
		ck, err := http.ParseSetCookie(line)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		if ck.Name != "XSRF-TOKEN" {
			continue
		}
		v, err := url.QueryUnescape(ck.Value)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

// TestRotateToken_SameSessionRefreshesTheRequestCache pins that a
// rotation which keeps the session id replaces the token the request
// cache holds for it: the cookie written after the rotation, and every
// later read, carry the new token, and the next submit presenting it is
// accepted.
func TestRotateToken_SameSessionRefreshesTheRequestCache(t *testing.T) {
	const id = "sess-1"
	cfg := DefaultConfig()
	cfg.Store = stores.NewMemoryStore()
	cfg.SessionIDResolver = func(*http.Request) (string, error) { return id, nil }
	c, err := NewE(cfg)
	if err != nil {
		t.Fatalf("NewE: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(WithCSRFTokenState(r.Context(), c))

	before, err := TokenForRequest(r)
	if err != nil || before == "" {
		t.Fatalf("read before the rotation: %q, %v", before, err)
	}
	if err := c.RotateToken(r.Context(), id, id); err != nil {
		t.Fatalf("RotateToken: %v", err)
	}
	stored, err := cfg.Store.Get(context.Background(), id)
	if err != nil || stored == "" {
		t.Fatalf("store get: %q, %v", stored, err)
	}
	if UnmaskToken(before) == stored {
		t.Fatal("premise: the rotation stored the token it replaced")
	}

	w := httptest.NewRecorder()
	c.WriteXSRFCookie(r.Context(), w, id)
	cookies := xsrfCookieValues(t, w.Header())
	if len(cookies) != 1 {
		t.Fatalf("XSRF-TOKEN lines = %d, want 1", len(cookies))
	}
	if UnmaskToken(cookies[0]) != stored {
		t.Fatal("the cookie written after a same-id rotation carries the replaced token")
	}
	after, err := TokenForRequest(r)
	if err != nil {
		t.Fatalf("read after the rotation: %v", err)
	}
	if after != cookies[0] {
		t.Fatalf("read after the rotation %q differs from the cookie %q", after, cookies[0])
	}

	post := httptest.NewRequest(http.MethodPost, "/", nil)
	post.Header.Set("X-CSRF-Token", cookies[0])
	pw := httptest.NewRecorder()
	c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(pw, post)
	if pw.Code != http.StatusOK {
		t.Fatalf("submit with the cookie after the rotation: %d, want 200", pw.Code)
	}
}

// TestRotateToken_QueuedBootstrapCookieFollowsTheRotation pins that the
// safe-method bootstrap cookie queued behind the session save carries the
// token the session holds when it is delivered: a same-id rotation by the
// handler after the bootstrap was queued is not undone by a later line
// carrying the replaced token.
func TestRotateToken_QueuedBootstrapCookieFollowsTheRotation(t *testing.T) {
	const id = "sess-1"
	var queued []func(http.ResponseWriter)
	cfg := DefaultConfig()
	cfg.Store = stores.NewMemoryStore()
	cfg.SessionIDResolver = func(*http.Request) (string, error) { return id, nil }
	cfg.QueueAfterSessionSave = func(_ *http.Request, write func(http.ResponseWriter)) bool {
		queued = append(queued, write)
		return true
	}
	c, err := NewE(cfg)
	if err != nil {
		t.Fatalf("NewE: %v", err)
	}

	var rendered string
	w := httptest.NewRecorder()
	c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := c.RotateToken(r.Context(), id, id); err != nil {
			t.Errorf("RotateToken: %v", err)
			return
		}
		c.WriteXSRFCookie(r.Context(), w, id)
		rendered, _ = TokenForRequest(r)
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if len(queued) != 1 {
		t.Fatalf("queued bootstrap writes = %d, want 1", len(queued))
	}
	// Deliver the queued bootstrap write the way the session seam does,
	// after the handler.
	for _, write := range queued {
		write(w)
	}

	stored, err := cfg.Store.Get(context.Background(), id)
	if err != nil || stored == "" {
		t.Fatalf("store get: %q, %v", stored, err)
	}
	if UnmaskToken(rendered) != stored {
		t.Fatal("the token rendered after the rotation is not the stored token")
	}
	cookies := xsrfCookieValues(t, w.Header())
	if len(cookies) == 0 {
		t.Fatal("no XSRF-TOKEN written")
	}
	for i, v := range cookies {
		if v != rendered {
			t.Fatalf("XSRF-TOKEN line %d = %q, want the rendered token %q", i, v, rendered)
		}
	}
}

// TestRotateToken_QueuedBootstrapCookieDroppedWhenTheSessionIsReplaced
// pins that a bootstrap cookie queued for a session the request then
// replaced (a rotation to a new id) writes nothing at delivery and mints
// no token for the retired id.
func TestRotateToken_QueuedBootstrapCookieDroppedWhenTheSessionIsReplaced(t *testing.T) {
	var queued []func(http.ResponseWriter)
	cfg := DefaultConfig()
	cfg.Store = stores.NewMemoryStore()
	cfg.SessionIDResolver = func(*http.Request) (string, error) { return "old-id", nil }
	cfg.QueueAfterSessionSave = func(_ *http.Request, write func(http.ResponseWriter)) bool {
		queued = append(queued, write)
		return true
	}
	c, err := NewE(cfg)
	if err != nil {
		t.Fatalf("NewE: %v", err)
	}

	w := httptest.NewRecorder()
	c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := c.RotateToken(r.Context(), "old-id", "new-id"); err != nil {
			t.Errorf("RotateToken: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if len(queued) != 1 {
		t.Fatalf("queued bootstrap writes = %d, want 1", len(queued))
	}
	for _, write := range queued {
		write(w)
	}
	if got := xsrfCookieValues(t, w.Header()); len(got) != 0 {
		t.Fatalf("the bootstrap for the replaced session wrote %v, want nothing", got)
	}
	if tok, _ := cfg.Store.Get(context.Background(), "old-id"); tok != "" {
		t.Fatal("delivery minted a token for the retired session id")
	}
}
