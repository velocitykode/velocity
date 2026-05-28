package guards

import (
	"net/http"
	"sync"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/router"
)

// preCommitHooker is the optional capability the save-at-end middleware
// uses to register a pre-commit hook on the response writer. *router.
// responseWriter implements it; other implementations (test recorders,
// custom wrappers) fall through to the defer-fallback save path so the
// middleware still functions, it just cannot intercept header commit.
type preCommitHooker interface {
	BeforeFirstWrite(fn func())
}

// SessionMiddleware returns a router.MiddlewareFunc that enables Laravel
// StartSession-equivalent semantics: every request is given a sessionHolder
// (via WithSessionContext) so SessionGuard.getSession can cache the
// resolved session for the lifetime of the request; BEFORE the response
// headers are committed AND as a defer fallback, the holder is consulted
// and, if a session was touched and mutated, it is saved to the response
// writer.
//
// Why two save sites:
//
//   - Pre-commit hook (preferred). The router's *responseWriter exposes
//     BeforeFirstWrite, which fires once before the first WriteHeader
//     or Write call. Set-Cookie lands in the same response that the
//     handler is about to flush. This is the only site that works for
//     handlers using c.JSON / c.Text / c.Redirect / direct writes,
//     because those commit headers from inside the handler body and the
//     post-handler save (line below) would write to an already-flushed
//     ResponseWriter with no effect.
//
//   - Post-handler defer fallback. Handlers that never write any output
//     (return after pure session mutation, e.g. a CSRF-only refresh
//     endpoint) never trip the pre-commit hook. The defer-path save
//     covers them, and httptest.ResponseRecorder paths (which don't
//     implement preCommitHooker but do accept late header writes).
//
// Without this middleware every ctx.Auth().Session(r).Put("k", v) and
// every Flash() write is silently lost because the framework never calls
// session.Save(w) on its own.
//
// Auto-installed by velocity.bootstrap() when the active guard is a
// *SessionGuard; consumers do not normally need to wire this themselves.
func (g *SessionGuard) SessionMiddleware() router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			// Replace the request with one carrying a sessionHolder so
			// any guard call inside the handler caches its session
			// lookup AND so we can recover the session after the
			// handler returns.
			//
			// If the holder is already present (e.g. a nested mount
			// installed the middleware twice), preserve the outer one
			// so the post-handler save still sees writes performed
			// before the inner middleware re-wrapped.
			r := c.Request
			if _, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); !ok {
				r = WithSessionContext(r)
				c.Request = r
			}

			// Eagerly bind a session to the request so anonymous-but-
			// stateful concerns (CSRF token mint, flash bag, anything
			// that wants a stable per-visitor id) have something to bind
			// to before the handler runs. Without this the lazy
			// SessionGuard.getSession path means a handler that never
			// touches the session (a plain Inertia page render, a static
			// dashboard, the login form GET) leaves the response with no
			// Set-Cookie, so the next POST arrives with no session id and
			// CSRF middleware has nothing to validate against (419 on the
			// first state-changing request).
			//
			// Order: load existing session first; only Create on miss so
			// we never overwrite a returning visitor's id. The created
			// session is marked modified so the doSave path below writes
			// the cookie even when the handler never touched the bag.
			ensureSession(g, c.Request)

			// saved guards both the pre-commit hook AND the defer
			// fallback so the session writes Set-Cookie at most once
			// per request. Without this gate a handler that calls
			// WriteHeader explicitly + the defer-fallback would issue
			// two Set-Cookie headers (one fresh, one stale).
			var saved sync.Once
			doSave := func() {
				saved.Do(func() {
					session := sessionFromHolder(c.Request)
					if session == nil {
						return
					}
					// Skip work when no mutation occurred. The
					// modifiedSession capability covers
					// *auth.BaseSession and the cookie store's
					// wrapper; sessions that do not expose the
					// capability fall through to an
					// unconditional Save (cheaper than
					// reflection, and CookieStore.Save itself
					// short-circuits on !IsModified() too).
					if ms, ok := session.(modifiedSession); ok {
						if !ms.IsModified() && !ms.IsDestroyed() {
							return
						}
					}
					_ = saveSessionFromMiddleware(g, c.Response, session)
				})
			}

			// Pre-commit hook: fires once just before the first
			// WriteHeader/Write/Hijack commits the response. This is
			// the load-bearing site for handlers that produce output
			// (c.JSON, c.Redirect, c.Text, ...).
			if h, ok := c.Response.(preCommitHooker); ok {
				h.BeforeFirstWrite(doSave)
			}

			err := next(c)

			// Defer-style fallback: covers handlers that never wrote
			// anything (so the pre-commit hook never fired) AND
			// response writers that did not implement
			// preCommitHooker (test recorders, custom wrappers).
			// sync.Once ensures we never double-save.
			doSave()
			return err
		}
	}
}

// ensureSession is the eager-bootstrap helper used by SessionMiddleware.
// It triggers the guard's normal getSession path, which loads an
// existing session from cookie OR mints a fresh one via
// auth.GetSessionFromRequest's store.Create("") fallback. The session
// is cached in the request-scoped sessionHolder so downstream
// getSession callers observe the same instance, and (for freshly
// minted ids) BaseSession sets modified=true so the post-handler
// doSave path writes the Set-Cookie even when the handler never
// touches the bag.
//
// Exists as a package-level var so test fixtures that stub guard
// internals can override it, mirroring saveSessionFromMiddleware's
// seam.
var ensureSession = func(g *SessionGuard, r *http.Request) {
	_ = g.getSession(r)
}

// saveSessionFromMiddleware is a small indirection so tests can override
// the save path without reaching into router/http internals. It exists
// solely to keep SessionMiddleware ergonomic to unit-test alongside the
// store implementation it drives.
var saveSessionFromMiddleware = func(g *SessionGuard, w http.ResponseWriter, s auth.Session) error {
	if err := s.Save(w); err != nil {
		g.logWarn("velocity/auth: save-at-end middleware: session save failed", "session_id", s.ID(), "error", err)
		return err
	}
	return nil
}
