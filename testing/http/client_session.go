package http

// Session test glue for TestClient / TestResponse.
//
// These helpers mirror the real-session pattern already used by ActingAs
// (client_auth.go): seed and read sessions through the SAME scheme store and
// crypto the production code path uses, never by fabricating cookies. A
// hand-rolled cookie would not decrypt in the store, so every seed below goes
// through scheme.Session(...).Save and every read goes through scheme.Session on
// a request carrying the real, encrypted cookie.
//
// Scope and the seams that bound it:
//
//   - WithSession seeds arbitrary session keys (the session-store analogue of
//     ActingAs seeding the authenticated user).
//   - AssertSessionHasErrors reads the validation-error bag back from the
//     encrypted "_velocity_errors" flash cookie. There is NO server-side
//     session store for flash data in velocity: validation errors live only in
//     that cookie (see bond/flash.go + router.SealFlash/OpenFlash), so the
//     cookie IS the realistic readback path. Decryption needs the app encryptor
//     (the same key the router sealed the cookie with); it is passed to the
//     assertion explicitly (resp.AssertSessionHasErrors(enc, "email")) so this
//     glue holds no encryptor state on TestClient/TestResponse.
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
	"net/http"
	"net/http/httptest"
	"reflect"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/router"
)

// WithSession seeds arbitrary session keys for subsequent requests under the
// given scheme. It builds a session through the scheme's own store (the same path
// production code and Login take), Puts each key/value, and persists so the
// resulting encrypted Set-Cookie is captured into the client jar exactly like
// ActingAs captures the login cookie. Later requests replay that cookie, so the
// router sees a genuine, decryptable session.
//
// The scheme is taken as a parameter (TestClient holds none), consistent with
// ActingAs. An empty data map writes nothing: the store skips an unmodified
// session, so no cookie is captured.
func (c *TestClient) WithSession(scheme *schemes.SessionScheme, data map[string]any) *TestClient {
	c.t.Helper()

	if scheme == nil {
		c.t.Errorf("WithSession: a non-nil *schemes.SessionScheme is required")
		return c
	}

	// A session cache must be attached or the scheme's per-request caching
	// no-ops; mirrors ActingAs's seed request.
	w := httptest.NewRecorder()
	req := schemes.WithSessionContext(httptest.NewRequest(http.MethodGet, "/", nil))

	session := scheme.Session(req)
	if session == nil {
		c.t.Errorf("WithSession: scheme returned no session for the seed request")
		return c
	}

	for key, value := range data {
		session.Put(key, value)
	}

	if err := session.Save(w); err != nil {
		c.t.Errorf("WithSession: session.Save failed: %v", err)
		return c
	}

	c.captureSessionCookies(w)
	return c
}

// AssertSessionHasErrors asserts that the response carried a decryptable
// "_velocity_errors" flash cookie containing each named field. Decryption uses
// enc (the same key the router sealed the cookie with); the bag is
// AEAD-encrypted, so without the matching encryptor the cookie cannot be opened.
//
// A nil encryptor, a missing cookie, a decrypt/authentication failure, or a
// non-object payload is reported as a clean failure via t.Errorf, never a panic,
// matching the safe handling in bond/flash.go.
func (r *TestResponse) AssertSessionHasErrors(enc crypto.Encryptor, fields ...string) *TestResponse {
	r.t.Helper()

	if enc == nil {
		r.t.Errorf("AssertSessionHasErrors: a non-nil crypto.Encryptor is required to open the flash error bag")
		return r
	}

	bag := r.openFlashErrors(enc)
	if bag == nil {
		r.t.Errorf("AssertSessionHasErrors: no decryptable %q flash cookie on the response", router.FlashErrorsCookie)
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

// openFlashErrors returns the decoded "_velocity_errors" bag from the response
// cookies, or nil when the cookie is absent or fails to open. Decryption reuses
// router.OpenFlash (the same helper bond/flash.go uses on the read path); crypto
// is never reimplemented here.
func (r *TestResponse) openFlashErrors(enc crypto.Encryptor) map[string]any {
	var sealed string
	for _, cookie := range r.recorder.Result().Cookies() {
		if cookie.Name == router.FlashErrorsCookie {
			sealed = cookie.Value
			break
		}
	}
	if sealed == "" {
		return nil
	}

	value, err := router.OpenFlash(enc, router.FlashErrorsCookie, sealed)
	if err != nil {
		return nil
	}
	bag, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return bag
}

// mapKeys returns the keys of m, for failure messages.
func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
