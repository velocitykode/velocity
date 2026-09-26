package schemes

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/crypto"
)

type errRandReader struct{ err error }

func (e errRandReader) Read(p []byte) (int, error) { return 0, e.err }

func withRememberRand(t *testing.T, r io.Reader) func() {
	t.Helper()
	orig := rememberRandReader
	rememberRandReader = r
	return func() { rememberRandReader = orig }
}

func newRememberEncryptor(t *testing.T) crypto.Encryptor {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    "0123456789abcdef0123456789abcdef",
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("crypto.NewEncryptor: %v", err)
	}
	return enc
}

// mockRememberUser lets us observe what SetRememberToken stores.
type mockRememberUser struct {
	id            string
	password      string
	rememberToken string
}

func (u *mockRememberUser) GetAuthIdentifier() interface{} { return u.id }
func (u *mockRememberUser) GetAuthPassword() string        { return u.password }
func (u *mockRememberUser) GetRememberToken() string       { return u.rememberToken }
func (u *mockRememberUser) SetRememberToken(tok string)    { u.rememberToken = tok }

type mockRememberStore struct {
	updated string
}

func (p *mockRememberStore) FindByID(interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (p *mockRememberStore) FindByCredentials(map[string]interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (p *mockRememberStore) ValidateCredentials(auth.Authenticatable, map[string]interface{}) bool {
	return false
}
func (p *mockRememberStore) UpdateRememberToken(u auth.Authenticatable, tok string) error {
	p.updated = tok
	u.SetRememberToken(tok)
	return nil
}

func TestGenerateRememberToken_RandFailure(t *testing.T) {
	restore := withRememberRand(t, errRandReader{err: errors.New("boom")})
	defer restore()
	got, err := generateRememberToken()
	if err == nil {
		t.Fatal("expected error from generateRememberToken")
	}
	if got != "" {
		t.Errorf("expected empty token on failure, got %q", got)
	}
	if !strings.HasPrefix(err.Error(), "velocity/auth:") {
		t.Errorf("error missing velocity/auth prefix: %v", err)
	}
}

func TestSetRememberCookie_StoresHashedToken(t *testing.T) {
	enc := newRememberEncryptor(t)
	userStore := &mockRememberStore{}
	g := func() *SessionScheme {
		g := &SessionScheme{
			config:    auth.SessionConfig{Name: "sess", IdleLifetime: 60},
			encryptor: enc,
		}
		g.userStore.Store(&userStoreHolder{p: userStore})
		g.throttler.Store(&throttlerHolder{t: auth.NoopLoginThrottler{}})
		return g
	}()
	user := &mockRememberUser{id: "u1"}
	w := httptest.NewRecorder()

	if err := g.setRememberCookie(context.Background(), w, user); err != nil {
		t.Fatalf("setRememberCookie: %v", err)
	}

	// The stored token must be a hex sha256 digest (64 chars), NOT the raw
	// base64 token that went into the cookie.
	if len(user.GetRememberToken()) != 64 {
		t.Errorf("expected sha256-hex (64 chars), got %q (%d)", user.GetRememberToken(), len(user.GetRememberToken()))
	}
	if userStore.updated != user.GetRememberToken() {
		t.Error("user store should have been called with the hashed token")
	}

	// The cookie lives the remember lifetime (default 30 days), not the
	// 60-minute idle lifetime.
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	if cookies[0].MaxAge != defaultRememberSeconds {
		t.Errorf("cookie MaxAge = %d, want %d (remember lifetime)", cookies[0].MaxAge, defaultRememberSeconds)
	}
}

// uintIDUser mirrors a user loaded from an integer primary key, where
// auth.NormalizeID hands back a uint (not a string). This is
// the default app shape; a prior bare .(string) assertion in
// setRememberCookie failed here and silently broke remember-me.
type uintIDUser struct {
	id            uint
	password      string
	rememberToken string
}

func (u *uintIDUser) GetAuthIdentifier() interface{} { return u.id }
func (u *uintIDUser) GetAuthPassword() string        { return u.password }
func (u *uintIDUser) GetRememberToken() string       { return u.rememberToken }
func (u *uintIDUser) SetRememberToken(tok string)    { u.rememberToken = tok }

func TestSetRememberCookie_NonStringIdentifier(t *testing.T) {
	enc := newRememberEncryptor(t)
	userStore := &mockRememberStore{}
	g := &SessionScheme{
		config:    auth.SessionConfig{Name: "sess", IdleLifetime: 60},
		encryptor: enc,
	}
	g.userStore.Store(&userStoreHolder{p: userStore})
	g.throttler.Store(&throttlerHolder{t: auth.NoopLoginThrottler{}})

	user := &uintIDUser{id: 42}
	w := httptest.NewRecorder()

	if err := g.setRememberCookie(context.Background(), w, user); err != nil {
		t.Fatalf("setRememberCookie with uint identifier: %v", err)
	}

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	decrypted, err := enc.Decrypt(cookies[0].Value)
	if err != nil {
		t.Fatalf("decrypt cookie: %v", err)
	}
	// Cookie value is "userID|token"; the uint id must round-trip as "42".
	if id := strings.SplitN(decrypted, "|", 2)[0]; id != "42" {
		t.Errorf("encoded user id = %q, want %q", id, "42")
	}
}

// The remember cookie's lifetime is its own: a configured RememberLifetime
// sets its Max-Age, and a session with no idle timeout (a browser-session
// cookie) still remembers its user.
func TestSetRememberCookie_UsesRememberLifetime(t *testing.T) {
	tests := []struct {
		name     string
		config   auth.SessionConfig
		wantSecs int
	}{
		{"configured remember lifetime", auth.SessionConfig{Name: "sess", IdleLifetime: 120, RememberLifetime: 7 * 24 * 60}, 7 * 24 * 60 * 60},
		{"no idle timeout", auth.SessionConfig{Name: "sess", IdleLifetime: 0}, defaultRememberSeconds},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &SessionScheme{config: tt.config, encryptor: newRememberEncryptor(t)}
			g.userStore.Store(&userStoreHolder{p: &mockRememberStore{}})
			g.throttler.Store(&throttlerHolder{t: auth.NoopLoginThrottler{}})
			w := httptest.NewRecorder()
			if err := g.setRememberCookie(context.Background(), w, &mockRememberUser{id: "u1"}); err != nil {
				t.Fatalf("setRememberCookie: %v", err)
			}
			cookies := w.Result().Cookies()
			if len(cookies) != 1 || cookies[0].MaxAge != tt.wantSecs {
				t.Fatalf("remember cookies = %+v, want one with MaxAge %d", cookies, tt.wantSecs)
			}
		})
	}
}

func TestCheckRememberCookie_ComparesHashedToken(t *testing.T) {
	enc := newRememberEncryptor(t)
	// Store a hash matching a known raw token.
	rawToken := "plain-raw-token-value"
	hashed := hashRememberToken(rawToken)
	user := &mockRememberUser{id: "u1", rememberToken: hashed}

	g := func() *SessionScheme {
		g := &SessionScheme{
			config:    auth.SessionConfig{Name: "sess", IdleLifetime: 60},
			encryptor: enc,
		}
		g.userStore.Store(&userStoreHolder{p: &mockRememberStore{}})
		g.throttler.Store(&throttlerHolder{t: auth.NoopLoginThrottler{}})
		return g
	}()
	// Install a lookup that returns our user.
	g.SetUserStore(&remLookupStore{user: user})

	// Encrypt cookie value "userID|rawToken".
	value := "u1|" + rawToken
	encrypted, err := enc.Encrypt(value)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "remember_sess", Value: encrypted})

	got := g.checkRememberCookie(r)
	if got == nil {
		t.Fatal("expected non-nil user for valid hashed token")
	}
	if got.GetAuthIdentifier() != "u1" {
		t.Errorf("unexpected user id: %v", got.GetAuthIdentifier())
	}
}

type remLookupStore struct {
	user auth.Authenticatable
}

func (p *remLookupStore) FindByID(interface{}) (auth.Authenticatable, error) {
	return p.user, nil
}
func (p *remLookupStore) FindByCredentials(map[string]interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (p *remLookupStore) ValidateCredentials(auth.Authenticatable, map[string]interface{}) bool {
	return false
}
func (p *remLookupStore) UpdateRememberToken(auth.Authenticatable, string) error { return nil }

// Ctx-suffixed shims for auth.UserStore, added in Sweep 1b.
func (p *mockRememberStore) FindByIDCtx(_ context.Context, id interface{}) (auth.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *remLookupStore) FindByIDCtx(_ context.Context, id interface{}) (auth.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *mockRememberStore) FindByCredentialsCtx(_ context.Context, credentials map[string]interface{}) (auth.Authenticatable, error) {
	return p.FindByCredentials(credentials)
}
func (p *remLookupStore) FindByCredentialsCtx(_ context.Context, credentials map[string]interface{}) (auth.Authenticatable, error) {
	return p.FindByCredentials(credentials)
}
func (p *mockRememberStore) UpdateRememberTokenCtx(_ context.Context, user auth.Authenticatable, token string) error {
	return p.UpdateRememberToken(user, token)
}
func (p *remLookupStore) UpdateRememberTokenCtx(_ context.Context, user auth.Authenticatable, token string) error {
	return p.UpdateRememberToken(user, token)
}
