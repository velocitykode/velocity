package http

// Session test glue for TestClient / TestResponse.
//
// These helpers mirror the real-session pattern already used by ActingAs
// (client_auth.go): seed and read sessions through the SAME scheme store and
// crypto the production code path uses, never by fabricating cookies. A
// hand-rolled cookie would not decrypt in the store, so every seed below goes
// through the scheme's SessionMiddleware save and every read goes through
// scheme.Session on a request carrying the real, encrypted cookie.
//
// Scope and the seams that bound it:
//
//   - WithSession seeds arbitrary session keys (the session-store analogue of
//     ActingAs seeding the authenticated user).
//   - AssertSessionHasErrors reads the validation errors back from the session
//     flash bag (router.FlashErrorsKey) of the session the response saved:
//     router.Context.FlashErrors flashes them into the session, so the
//     session cookie the response sets IS the realistic readback path. The
//     scheme is passed to the assertion explicitly
//     (resp.AssertSessionHasErrors(scheme, "email")) so this glue holds no
//     scheme state on TestClient/TestResponse.
//   - AssertSessionHas / AssertSessionMissing read the client's current session
//     (the one seeded by WithSession) by replaying the client's own cookies onto
//     a probe request and asking the scheme for the session (the same path as
//     authProbeRequest). They are client-level rather than response-level
//     because a normal request need not re-save the session, so the response may
//     carry no Set-Cookie even though later requests still send the seeded
//     session. Values are compared with reflect.DeepEqual. CAVEAT: the cookie
//     store serializes session data as JSON (auth/drivers/session/cookie.go), so
//     values round-trip through JSON types: a seeded int reads back as float64,
//     a []int as []float64, and so on. Assert against the post-JSON type (e.g.
//     float64(7), not 7).
//
// Deliberately NOT provided:
//
//   - WithoutMiddleware / WithoutCsrf: a prebuilt router http.Handler bakes its
//     middleware chain in at build time, so there is no per-request bypass to
//     reach from a test client. There is, however, normally no need to: when the
//     CSRF instance is built with a testing Env (csrf.Config.Env, which the
//     framework copies from the app Config.Env - velocitytest.NewApp uses
//     "testing"), the csrf middleware skips token validation on unsafe
//     requests. Outside a testing Env, disable CSRF at router-build time or
//     inject a valid token.
//   - InjectCSRFToken: minting a CSRF token is not trivially supported from
//     outside the pipeline. csrf.TokenForRequest (csrf/request_token.go) reads
//     a per-request token state that only csrf middleware installs, and the
//     token is bound to the session id with a matching XSRF-TOKEN cookie. There
//     is no stable public seam to forge that from a test client, so it is
//     omitted rather than half-implemented.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/router"
)

// errNoSeedSession reports that the scheme resolved no session for
// WithSession's seed request.
var errNoSeedSession = errors.New("scheme returned no session for the seed request")

// WithSession seeds arbitrary session keys for subsequent requests under the
// given scheme. It builds a session through the scheme's own store (the same path
// production code and Login take), Puts each key/value, and persists so the
// resulting encrypted Set-Cookie is captured into the client jar exactly like
// ActingAs captures the login cookie. Later requests replay that cookie, so the
// router sees a genuine, decryptable session.
//
// The scheme is taken as a parameter (TestClient holds none), consistent with
// ActingAs. An empty data map writes nothing, so no cookie is captured.
func (c *TestClient) WithSession(scheme *schemes.SessionScheme, data map[string]any) *TestClient {
	c.t.Helper()

	if scheme == nil {
		c.t.Errorf("WithSession: a non-nil *schemes.SessionScheme is required")
		return c
	}
	if len(data) == 0 {
		return c
	}

	// The seed request runs inside the session middleware, which saves
	// the session; mirrors ActingAs's seed request.
	w, err := runInSession(scheme, func(rc *router.Context) error {
		session := scheme.Session(rc.Request)
		if session == nil {
			return errNoSeedSession
		}
		for key, value := range data {
			session.Put(key, value)
		}
		return nil
	})
	if err != nil {
		c.t.Errorf("WithSession: %v", err)
		return c
	}

	c.captureSessionCookies(w)
	return c
}

// AssertSessionHasErrors asserts that the session the response saved under
// the given scheme carries flashed validation errors for each named field, the
// errors the next page receives as its "errors" prop. A failure flashed under
// a named error bag counts its fields as present, the same as on the page.
// The session is read through the scheme from the response's own Set-Cookie,
// so it must be the scheme (store and key) the app saved it with.
//
// A nil scheme, a response that saved no session, a session the scheme cannot
// open, or a session without flashed errors is reported as a clean failure via
// t.Errorf, never a panic. The read does not consume the errors.
func (r *TestResponse) AssertSessionHasErrors(scheme *schemes.SessionScheme, fields ...string) *TestResponse {
	r.t.Helper()

	if scheme == nil {
		r.t.Errorf("AssertSessionHasErrors: a non-nil *schemes.SessionScheme is required to read the session flash bag")
		return r
	}

	bag := r.flashedErrors(scheme)
	if bag == nil {
		r.t.Errorf("AssertSessionHasErrors: the response saved no session carrying flashed errors (%q)", router.FlashErrorsKey)
		return r
	}

	for _, field := range fields {
		if _, ok := bag[field]; !ok {
			r.t.Errorf("AssertSessionHasErrors: expected error bag to contain field %q, present keys: %v", field, mapKeys(bag))
		}
	}
	return r
}

// AssertSessionHas asserts that the client's current session, read back through
// the scheme from the client's own cookies, has key bound to value. Values are
// compared with reflect.DeepEqual after a JSON round-trip through the cookie
// store, so assert against the post-JSON type (numbers read back as float64).
//
// It is client-level (not response-level): the seeded session lives in the
// client's cookie jar, and a normal request need not re-emit a Set-Cookie, so a
// response may carry no session even though later requests still send it.
func (c *TestClient) AssertSessionHas(scheme *schemes.SessionScheme, key string, value any) *TestClient {
	c.t.Helper()

	if scheme == nil {
		c.t.Errorf("AssertSessionHas: a non-nil *schemes.SessionScheme is required")
		return c
	}

	session := c.sessionFromClient(scheme)
	if session == nil {
		c.t.Errorf("AssertSessionHas: client carried no readable session for key %q", key)
		return c
	}
	if !session.Has(key) {
		c.t.Errorf("AssertSessionHas: expected session to have key %q, but it is missing", key)
		return c
	}
	if actual := session.Get(key); !reflect.DeepEqual(actual, value) {
		c.t.Errorf("AssertSessionHas: key %q: expected %#v (%T), got %#v (%T)", key, value, value, actual, actual)
	}
	return c
}

// AssertSessionMissing asserts that the client's current session does not have
// key. A client that carried no session cookie trivially satisfies this (the
// key cannot be present), so it passes.
func (c *TestClient) AssertSessionMissing(scheme *schemes.SessionScheme, key string) *TestClient {
	c.t.Helper()

	if scheme == nil {
		c.t.Errorf("AssertSessionMissing: a non-nil *schemes.SessionScheme is required")
		return c
	}

	session := c.sessionFromClient(scheme)
	if session == nil {
		return c
	}
	if session.Has(key) {
		c.t.Errorf("AssertSessionMissing: expected session to NOT have key %q, but it is present with value %#v", key, session.Get(key))
	}
	return c
}

// sessionFromClient asks the scheme for the session carried by the client's
// current cookies. It reuses authProbeRequest (the same probe path AssertGuest /
// AssertAuthenticated use), so reads go through the real cookie jar rather than a
// single response's Set-Cookie.
func (c *TestClient) sessionFromClient(scheme *schemes.SessionScheme) auth.Session {
	if scheme == nil {
		return nil
	}
	return scheme.Session(c.authProbeRequest())
}

// flashedErrors returns the errors flashed into the session the response
// saved, as the page sees them, or nil when the response set no session the
// scheme can open or the session carries no flashed errors. The session is
// opened through the scheme on a probe request carrying the response's
// cookies (the path the next request takes), so crypto and the store are
// never reimplemented here; the probe session is discarded, so the read does
// not consume the errors.
func (r *TestResponse) flashedErrors(scheme *schemes.SessionScheme) map[string]any {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, cookie := range r.recorder.Result().Cookies() {
		if cookie.MaxAge >= 0 && cookie.Value != "" {
			req.AddCookie(cookie)
		}
	}
	session := scheme.Session(schemes.WithSessionContext(req))
	if session == nil {
		return nil
	}
	errs, _ := session.GetFlash(router.FlashErrorsKey).(map[string]any)
	return errs
}

// mapKeys returns the keys of m, for failure messages.
func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
