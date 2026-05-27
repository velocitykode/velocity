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
// The lazy-load pattern is "compute once, succeed or remember the
// failure": once loaded=true, subsequent calls return the cached
// (token, err) pair verbatim. This guarantees byte-identical tokens
// across every reader on the same request. A transient store failure
// returns the same error on every subsequent call within the request,
// which is the desired behaviour: callers see one stable signal per
// request rather than a flaky pair where the second read drifts.
type requestTokenState struct {
	csrf *CSRF

	mu     sync.Mutex
	loaded bool
	token  string
	err    error
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

// TokenForRequest returns the CSRF token for the request's session,
// memoized for the request lifetime so multiple readers see
// byte-identical values.
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
//   - (token, nil) on the happy path. token is the value GetToken would
//     have produced; never empty.
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
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.loaded {
		return state.token, state.err
	}
	state.loaded = true

	c := state.csrf
	if c == nil || c.config == nil {
		state.err = ErrNoStore
		return "", state.err
	}

	sessionID, err := c.getSessionIDQuiet(r)
	if err != nil {
		// Anonymous request, no session yet, no token to mint.
		// Return empty + nil per the documented contract. Cache the
		// outcome so a second reader (e.g. the bond shared props
		// function later in the same request) sees the same "no
		// token yet" answer instead of paying the same resolver call.
		state.token = ""
		state.err = nil
		return "", nil
	}

	token, err := c.GetToken(sessionID)
	if err != nil {
		state.err = err
		return "", err
	}
	state.token = token
	return token, nil
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
