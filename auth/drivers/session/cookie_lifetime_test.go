package session

import (
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// TestCookieStore_Save_LifetimeZero_OmitsExpires covers audit M-07: when
// Lifetime <= 0 the operator wants a session-lifetime cookie. The previous
// code wrote Expires=time.Now() which browsers interpret as already
// expired and silently dropped the cookie. The cookie must now carry no
// Expires header and MaxAge=0 (RFC 6265 "session" cookie).
func TestCookieStore_Save_LifetimeZero_OmitsExpires(t *testing.T) {
	cfg := auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 0,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
	}
	store, err := NewCookieStore(cfg, &mockEncryptor{})
	if err != nil {
		t.Fatalf("NewCookieStore err = %v", err)
	}

	sess, _ := store.Create("id-zero-lifetime")
	sess.Put("k", "v")

	rec := httptest.NewRecorder()
	if err := store.Save(rec, sess); err != nil {
		t.Fatalf("Save err = %v", err)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if !c.Expires.IsZero() {
		t.Errorf("Expires must be zero when Lifetime <= 0, got %v", c.Expires)
	}
	if c.MaxAge != 0 {
		t.Errorf("MaxAge must be 0 when Lifetime <= 0 (RFC 6265 session cookie), got %d", c.MaxAge)
	}
}

// TestCookieStore_Save_LifetimePositive_SetsExpiresAndMaxAge confirms the
// regular path is unaffected by the M-07 fix.
func TestCookieStore_Save_LifetimePositive_SetsExpiresAndMaxAge(t *testing.T) {
	cfg := auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 30,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
	}
	store, err := NewCookieStore(cfg, &mockEncryptor{})
	if err != nil {
		t.Fatalf("NewCookieStore err = %v", err)
	}

	sess, _ := store.Create("id-positive-lifetime")
	sess.Put("k", "v")

	rec := httptest.NewRecorder()
	if err := store.Save(rec, sess); err != nil {
		t.Fatalf("Save err = %v", err)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Expires.IsZero() {
		t.Error("Expires must be set when Lifetime > 0")
	}
	if c.MaxAge != 30*60 {
		t.Errorf("MaxAge = %d, want %d", c.MaxAge, 30*60)
	}
}
