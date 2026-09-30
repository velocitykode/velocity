package csrf

import (
	"context"
	"errors"
	"net/http"
	"sync"
)

// tokenStateKey is the unexported context key under which the
// request-scoped CSRF token cache is stored. A struct{} type keeps the
// key unique to this package; collisions with other packages that
// inhabit r.Context() are impossible.
type tokenStateKey struct{}

// requestTokenState is the request-scoped cache referenced by
// TokenForRequest. It carries the *CSRF instance attached by the
// middleware AND a once-loaded token. All fields are protected by mu so
// fan-out goroutines reading the same request (e.g. async props
// builders) cannot race the lazy initialisation.
//
// Two readers per request is the documented hot path (CSRF middleware
// minting the XSRF-TOKEN cookie + the bond sharePropsFunc populating
// page.props.csrf_token), but the field set is small and the mutex
// cost is negligible compared to the Store.Get round-trip it elides.
//
// The lazy-load pattern is "compute once per session, succeed or
// remember the failure": once loaded=true, subsequent calls for the same
// session id return the cached (token, err) pair verbatim. This
// guarantees byte-identical tokens across every reader on the same
// request. A transient store failure returns the same error on every
// subsequent call within the request, which is the desired behaviour:
// callers see one stable signal per request rather than a flaky pair
// where the second read drifts.
//
// The cache follows the session the request is served under: sessionID
// is the id the cached pair was loaded for, and a read that resolves a
// different id (a sign-in or remember-me recall regenerated the session
// after an earlier read) loads again. Otherwise the page a recall
// renders would carry the token of the session the recall replaced,
// which the store no longer holds, and the first submit would 419.
//
// token holds the EMISSION form: the stored token wrapped in this
// request's mask (see MaskToken). Masking once at load time, rather
// than per reader, keeps the cookie and every props/meta reader
// byte-identical within the response while still giving each response
// a unique byte string.
type requestTokenState struct {
	csrf *CSRF

	mu        sync.Mutex
	loaded    bool
	sessionID string
	token     string
	err       error
}

// tokenFor returns the masked token for sessionID, from the cache when it
// was loaded for sessionID, else loaded through c.GetToken under ctx and
// cached. An empty sessionID caches the "no session, no token" answer.
func (s *requestTokenState) tokenFor(ctx context.Context, sessionID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded && s.sessionID == sessionID {
		return s.token, s.err
	}
	s.loaded, s.sessionID, s.token, s.err = true, sessionID, "", nil

	if sessionID == "" {
		return "", nil
	}
	c := s.csrf
	if c == nil || c.config == nil {
		s.err = ErrNoStore
		return "", s.err
	}
	token, err := c.GetToken(ctx, sessionID) //lock-held-ok: token store read under s.mu, removed by the single-flight token load
	if err != nil {
		s.err = err
		return "", err
	}
	// Cache the masked emission form, not the raw stored token: every
	// reader on this request (cookie write, meta tag, props) must emit
	// the same bytes, and those bytes must differ from every other
	// response's emission of the same stored token.
	masked, err := MaskToken(token)
	if err != nil {
		s.err = err
		return "", err
	}
	s.token = masked
	return masked, nil
}

// cachedFor returns the cached emission-form token when the cache holds a
// successful load for sessionID, without loading. It reports false once
// the cache moved on (the session was replaced, or a rotation retired
// sessionID), so a caller never mints a token for an id the request left.
func (s *requestTokenState) cachedFor(sessionID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded || s.sessionID != sessionID || s.err != nil || s.token == "" {
		return "", false
	}
	return s.token, true
}

// replaceAfterRotation records the token a successful rotation stored for
// newID. When the cache holds the token of oldID or newID (or nothing yet),
// it now holds newID's new token in this request's emission form, so the
// cookie written after the rotation, a queued bootstrap cookie delivered
// later and every later read carry the token the store holds. A rotation
// that keeps the id (oldID == newID) is covered the same way: keyed on the
// id alone, the cache would otherwise keep serving the replaced token. A
// cache loaded for an unrelated session is left alone.
func (s *requestTokenState) replaceAfterRotation(oldID, newID, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded && s.sessionID != oldID && s.sessionID != newID {
		return
	}
	masked, err := MaskToken(token)
	if err != nil {
		// Drop the entry: the next read loads the stored token again.
		s.loaded, s.sessionID, s.token, s.err = false, "", "", nil
		return
	}
	s.loaded, s.sessionID, s.token, s.err = true, newID, masked, nil
}

// withTokenState attaches a new requestTokenState to ctx, carrying the
// CSRF instance handle so package-level TokenForRequest(r) can resolve
// the token without the caller threading the *CSRF reference itself.
//
// Callers MUST replace the request with one carrying this context for
// the helper to observe the state (r = r.WithContext(withTokenState(...))).
// The CSRF Middleware does this transparently; consumer code does not
// need to call it directly.
//
// withTokenState is package-private: only the CSRF instance owns the
// attachment, so the public helper can never observe a forged state
// pointing at an attacker-controlled CSRF instance.
func withTokenState(ctx context.Context, c *CSRF) context.Context {
	return context.WithValue(ctx, tokenStateKey{}, &requestTokenState{csrf: c})
}

// tokenStateFromContext returns the state attached by the CSRF
// middleware, or nil when none is present (handler running outside the
// CSRF middleware path, unit test bypassing the middleware, etc.).
func tokenStateFromContext(ctx context.Context) *requestTokenState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(tokenStateKey{}).(*requestTokenState)
	return state
}

// ErrNoTokenState is returned by TokenForRequest when the request did
// not pass through the CSRF middleware (or an equivalent setup) and so
// no request-scoped CSRF state is attached. Callers that want to defer
// to the framework's standard middleware can treat this error as a
// no-op: render the page without a CSRF token and let the next GET (via
// the safe-method bootstrap path) seed one.
var ErrNoTokenState = errors.New("velocity/csrf: no request-scoped CSRF state on request (middleware did not run)")

// TokenForRequest returns the CSRF token for the request's session in
// its per-response MASKED form (see MaskToken), memoized for the
// request lifetime so multiple readers see byte-identical values.
//
// The returned value is safe to embed anywhere in the response (cookie,
// <meta> tag, page props) and validates like the raw token: the
// middleware unmasks before its constant-time comparison. Two requests
// for the same session receive different byte strings even though the
// stored token is unchanged, which defeats compression-oracle (BREACH)
// extraction of the token from response bodies. Do NOT compare the
// returned value against the stored token directly; unmask first with
// UnmaskToken or validate via the middleware.
//
// Why this exists: server-rendered pages frequently expose the CSRF
// token in two places per request, a <meta name="csrf-token"> tag in
// the rendered HTML head AND a page-prop like `csrf_token` consumed by
// a SPA's HTTP client (axios, fetch). Each reader used to call
// (*CSRF).GetToken(sessionID) independently. GetToken is idempotent on
// a healthy store, but the round-trip is not free, and two reads under
// transient store inconsistency could drift (mint twice, return
// different generated values), which surfaces as a 419 on the first
// POST because the client and server side disagree on which token is
// canonical.
//
// TokenForRequest collapses both reads onto a single Store.Get +
// optional Store.Set, then memoises the result in the request context
// so any number of downstream readers (template helpers,
// sharePropsFunc, Inertia share callbacks) see the same byte string.
//
// Lifetime: the cache is scoped to the request context. When the
// request completes, the context is cancelled and the cache is
// eligible for GC. Different concurrent requests get independent
// caches (one per request).
//
// Return values:
//   - (token, nil) on the happy path. token is the masked form of the
//     value GetToken would have produced; never empty.
//   - ("", nil) when the request carries no resolvable session id (the
//     caller is rendering an anonymous page; no CSRF token to embed
//     yet). The XSRF-TOKEN cookie will be seeded by the next
//     authenticated GET via the safe-method bootstrap path.
//   - ("", ErrNoTokenState) when the request did not pass through the
//     CSRF middleware. Treat as "no token available"; do not return 5xx.
//   - ("", err) on store failure. The same err is returned on every
//     subsequent TokenForRequest call within this request so callers
//     see a stable signal.
//
// The session id is resolved on every call and the cache is keyed on it:
// when a sign-in or a remember-me recall regenerated the session after an
// earlier read, the next read returns the token of the new session (see
// requestTokenState).
//
// Safe to call any number of times. Concurrent fan-out on the same
// request is supported via an internal mutex.
//
// Wire from middleware via WithCSRFTokenState or rely on the framework
// CSRF middleware which attaches the state automatically.
func TokenForRequest(r *http.Request) (string, error) {
	if r == nil {
		return "", ErrNoTokenState
	}
	state := tokenStateFromContext(r.Context())
	if state == nil {
		return "", ErrNoTokenState
	}
	c := state.csrf
	if c == nil || c.config == nil {
		return "", ErrNoStore
	}
	// Resolve on every read: the cache is keyed on the session id, so a
	// session regenerated earlier in the request (a sign-in or recall)
	// loads the token of the session the response is now served under.
	// An anonymous request (no session yet) caches the empty answer.
	sessionID, err := c.getSessionIDQuiet(r)
	if err != nil {
		sessionID = ""
	}
	return state.tokenFor(r.Context(), sessionID)
}

// WithCSRFTokenState attaches a request-scoped CSRF token cache to ctx
// so package-level TokenForRequest(r) can return memoised tokens for
// subsequent readers in the same request.
//
// The framework CSRF middleware calls this automatically; consumer code
// that bypasses the middleware (custom middleware stacks, test
// harnesses, gRPC bridges that still want to mint a CSRF token for the
// returned page) can call it directly to opt in.
//
// Calling more than once on the same context returns a NEW state
// pointing at the most-recently-supplied CSRF instance; the older
// state is shadowed (lookups land on the new pointer). This matches
// the standard context.WithValue shadowing semantics.
func WithCSRFTokenState(ctx context.Context, c *CSRF) context.Context {
	return withTokenState(ctx, c)
}
