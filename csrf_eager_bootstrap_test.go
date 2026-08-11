package velocity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/guards"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/csrf/stores"
	"github.com/velocitykode/velocity/router"
)

// TestCSRF_EagerBootstrap_AnonymousGETMintsTokenAndCookie pins the
// post-logout / first-anonymous-visit recovery path that lives in the
// framework's default SessionIDResolver.
//
// Scenario: an anonymous GET arrives with no incoming session cookie.
// SessionMiddleware's eager bootstrap mints a fresh session and caches
// it on the request holder. The framework's CSRF SessionIDResolver
// MUST consult that holder; otherwise the safe-method bootstrap path
// inside csrf.Middleware sees ErrNoSession, skips the XSRF-TOKEN
// write, and the user's next POST 419's with no token to echo. The
// pre-fix resolver read only the inbound cookie and failed exactly
// this flow.
//
// Asserts that after one anonymous GET through SessionMiddleware +
// csrf.Middleware:
//   - The response carries a Set-Cookie for the session cookie.
//   - The response carries a Set-Cookie for XSRF-TOKEN.
//   - The decoded XSRF-TOKEN value matches the token the csrf store
//     minted under the session id created by the eager bootstrap.
func TestCSRF_EagerBootstrap_AnonymousGETMintsTokenAndCookie(t *testing.T) {
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	sessionCookieName := "vel_session"
	userStore := &eagerStubStore{}
	sessionScheme, err := guards.NewSessionScheme(userStore, auth.SessionConfig{
		Name: sessionCookieName,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}

	// Replicate the resolver shape installed by velocity.New (see
	// app.go: default SessionIDResolver). Holder-first, cookie-decrypt
	// fallback. This is the unit under test.
	resolver := func(r *http.Request) (string, error) {
		if sess := guards.SessionFromRequest(r); sess != nil {
			if id := sess.ID(); id != "" {
				return id, nil
			}
		}
		c, err := r.Cookie(sessionCookieName)
		if err != nil || c.Value == "" {
			return "", csrf.ErrNoSession
		}
		plaintext, err := enc.Decrypt(c.Value)
		if err != nil {
			return "", csrf.ErrNoSession
		}
		var payload struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(plaintext), &payload); err != nil || payload.ID == "" {
			return "", csrf.ErrNoSession
		}
		return payload.ID, nil
	}

	csrfCfg := csrf.DefaultConfig()
	csrfCfg.SessionCookieName = sessionCookieName
	csrfCfg.SessionIDResolver = resolver
	csrfCfg.Store = stores.NewSessionStore()
	csrfInst := csrf.New(csrfCfg)

	// SessionMiddleware (eager bootstrap) -> csrf.Middleware -> noop.
	chain := sessionScheme.SessionMiddleware()(func(c *router.Context) error {
		csrfInst.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(c.Response, c.Request)
		return nil
	})

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	c := router.NewContext(rec, req)
	if err := chain(c); err != nil {
		t.Fatalf("middleware chain returned error: %v", err)
	}

	// Read directly from the live recorder header map. Result().Cookies()
	// snapshots headers at WriteHeader time, so any Set-Cookie added by
	// doSave's defer-fallback (which runs after the inner WriteHeader in
	// this fixture) is invisible to the snapshot. The production
	// router.responseWriter uses BeforeFirstWrite so save runs ahead of
	// commit; here we just inspect the post-handler header map directly.
	rawSetCookie := rec.Header().Values("Set-Cookie")
	cookies := parseSetCookies(rawSetCookie)

	var (
		xsrf          *http.Cookie
		sessionCookie *http.Cookie
	)
	for _, ck := range cookies {
		switch ck.Name {
		case "XSRF-TOKEN":
			xsrf = ck
		case sessionCookieName:
			sessionCookie = ck
		}
	}

	if sessionCookie == nil {
		t.Fatal("SessionMiddleware did not write the session Set-Cookie on an anonymous GET; eager bootstrap is broken")
	}
	if xsrf == nil {
		t.Fatal("csrf.Middleware did not emit XSRF-TOKEN on the safe-method bootstrap path; SessionIDResolver did not see the eager-bootstrapped session")
	}

	decoded, err := url.QueryUnescape(xsrf.Value)
	if err != nil {
		t.Fatalf("XSRF-TOKEN value not URL-encoded: %v", err)
	}
	resolvedID, err := resolver(c.Request)
	if err != nil {
		t.Fatalf("resolver after middleware: %v", err)
	}
	if resolvedID == "" {
		t.Fatal("resolver returned empty id after eager bootstrap; holder fallback is not wired")
	}
	stored, err := csrfInst.GetToken(resolvedID)
	if err != nil {
		t.Fatalf("GetToken(%q): %v", resolvedID, err)
	}
	// The cookie carries the per-response masked form (BREACH hardening);
	// unmask before comparing against the raw stored token.
	if unmasked := csrf.UnmaskToken(decoded); unmasked != stored {
		t.Errorf("XSRF-TOKEN cookie value (decoded=%q, unmasked=%q) does not match token stored under resolved session id %q (stored=%q)",
			decoded, unmasked, resolvedID, stored)
	}
}

// eagerStubStore is the minimum auth.UserStore needed to
// construct a SessionScheme; none of its methods run in the
// anonymous-bootstrap test path.
type eagerStubStore struct{}

func (eagerStubStore) FindByID(interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (eagerStubStore) FindByIDCtx(context.Context, interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (eagerStubStore) FindByCredentials(map[string]interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (eagerStubStore) FindByCredentialsCtx(context.Context, map[string]interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (eagerStubStore) ValidateCredentials(auth.Authenticatable, map[string]interface{}) bool {
	return false
}
func (eagerStubStore) UpdateRememberToken(auth.Authenticatable, string) error {
	return nil
}
func (eagerStubStore) UpdateRememberTokenCtx(context.Context, auth.Authenticatable, string) error {
	return nil
}

// parseSetCookies is a tiny inline replacement for http.ReadResponse's
// cookie parser, used because httptest.ResponseRecorder.Result()
// snapshots headers at WriteHeader time and would miss cookies added
// by post-WriteHeader middleware hooks.
func parseSetCookies(raw []string) []*http.Cookie {
	// Reuse the stdlib parser by faking a response object.
	resp := http.Response{Header: http.Header{"Set-Cookie": raw}}
	return resp.Cookies()
}
