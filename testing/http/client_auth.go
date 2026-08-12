package http

import (
	"net/http"
	"net/http/httptest"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
)

// ActingAs authenticates subsequent requests from the client as user under the
// given session scheme. It performs a real login against the scheme's session
// store (the same code path production login takes), captures the resulting
// session Set-Cookie, and replays that cookie on every later request.
//
// Only the session cookie is seeded. Mutating verbs (POST/PUT/PATCH/DELETE)
// routed through CSRF middleware still require a valid CSRF token or CSRF
// disabled - ActingAs does not exempt requests from CSRF protection.
func (c *TestClient) ActingAs(scheme *schemes.SessionScheme, user auth.Authenticatable) *TestClient {
	c.t.Helper()
	// A session cache must be attached to the request or the scheme's
	// per-request session caching no-ops and Login cannot persist.
	w := httptest.NewRecorder()
	req := schemes.WithSessionContext(httptest.NewRequest(http.MethodGet, "/", nil))

	if err := scheme.Login(w, req, user); err != nil {
		c.t.Errorf("ActingAs: scheme.Login failed: %v", err)
		return c
	}

	c.captureSessionCookies(w)
	return c
}

// ActingAsID authenticates subsequent requests as the user with the given
// identifier, resolving the user through the scheme's user store. It mirrors
// ActingAs but logs in by ID.
//
// As with ActingAs, only the session cookie is seeded; CSRF-protected mutating
// verbs still need a valid token or CSRF disabled.
func (c *TestClient) ActingAsID(scheme *schemes.SessionScheme, id interface{}) *TestClient {
	c.t.Helper()
	w := httptest.NewRecorder()
	req := schemes.WithSessionContext(httptest.NewRequest(http.MethodGet, "/", nil))

	if err := scheme.LoginByID(w, req, id); err != nil {
		c.t.Errorf("ActingAsID: scheme.LoginByID failed: %v", err)
		return c
	}

	c.captureSessionCookies(w)
	return c
}

// setCookie stores a cookie for all requests, replacing any existing cookie of
// the same name in place rather than appending a duplicate. Replaying two
// cookies with the same name would let the stale one (sent first in slice
// order) shadow the new one for stores that read the first match.
func (c *TestClient) setCookie(cookie *http.Cookie) {
	for i, existing := range c.cookies {
		if existing.Name == cookie.Name {
			c.cookies[i] = cookie
			return
		}
	}
	c.cookies = append(c.cookies, cookie)
}

// captureSessionCookies copies every Set-Cookie the login wrote on w into the
// client's cookie jar. Login regenerates the session id and writes the real,
// encrypted session cookie via session.Save - a fabricated cookie would not
// decrypt in the store, so the genuine Set-Cookie must be captured here. The
// cookie name comes from the scheme config, never hardcoded.
//
// Each captured cookie replaces any existing cookie of the same name rather
// than being appended. Otherwise a stale session cookie (or a second
// ActingAs/ActingAsID switching users) would sit ahead of the new login cookie
// in the slice and be replayed first, and the session store reads the first
// cookie matching the configured name - so the real login cookie would be
// ignored and the request would not authenticate as the requested user.
func (c *TestClient) captureSessionCookies(w *httptest.ResponseRecorder) {
	for _, cookie := range w.Result().Cookies() {
		c.setCookie(cookie)
	}
}

// AssertAuthenticated asserts that the client's current cookies authenticate a
// request under the given scheme. It builds a request carrying the client's
// cookies, wraps it with a session cache, and consults the scheme's Check.
func (c *TestClient) AssertAuthenticated(scheme *schemes.SessionScheme) *TestClient {
	c.t.Helper()
	if !scheme.Check(c.authProbeRequest()) {
		c.t.Errorf("AssertAuthenticated: expected an authenticated user, but the request is a guest")
	}
	return c
}

// AssertGuest asserts that the client's current cookies do NOT authenticate a
// request under the given scheme (i.e. the request is unauthenticated).
func (c *TestClient) AssertGuest(scheme *schemes.SessionScheme) *TestClient {
	c.t.Helper()
	if user := scheme.User(c.authProbeRequest()); user != nil {
		c.t.Errorf("AssertGuest: expected a guest, but request is authenticated as %v", user.GetAuthIdentifier())
	}
	return c
}

// authProbeRequest builds a GET request carrying the client's current cookies
// and a session cache, suitable for passing to scheme.Check / scheme.User. It
// applies cookies the same way doRequest does, then attaches the session
// context the scheme requires.
func (c *TestClient) authProbeRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}
	return schemes.WithSessionContext(req)
}
