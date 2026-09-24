package schemes

import (
	"net/http"
	"sync"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/router"
)

// preCommitHooker is the optional capability the save-at-end middleware
// uses to register a pre-commit hook on the response writer. The
// router's response writer implements it, and the router fires a hook
// nothing fired once its error boundary is done with the request; other
// implementations (test recorders, custom wrappers) fall through to the
// post-handler save so the middleware still functions, it just cannot
// intercept header commit.
type preCommitHooker interface {
	BeforeFirstWrite(fn func())
}

// SessionMiddleware returns a router.MiddlewareFunc that gives every
// request save-at-end session semantics: the request is given a sessionHolder
// (via WithSessionContext) so SessionScheme.getSession can cache the
// resolved session for the lifetime of the request; BEFORE the response
// headers are committed, the holder is consulted and, if a session was
// touched and mutated, it is saved to the response writer.
//
// Where the save happens:
//
//   - Pre-commit hook, on the router's response writer, which exposes
//     BeforeFirstWrite. The hook fires once before the first WriteHeader
//     or Write call, so Set-Cookie lands in the same response the
//     handler is about to flush (c.JSON / c.Text / c.Redirect / direct
//     writes commit headers from inside the handler body). The hook stays
//     armed after the handler returns: when the handler returned an
//     error, the router's error boundary writes the error response later,
//     and its first write fires the hook, so session changes the error
//     path makes (a flash the error page drains, a render rule's Put, the
//     intended-URL stash) are saved with it. When neither the handler nor
//     the error path writes anything, the router fires the hook once the
//     boundary is done, before net/http sends its implicit 200.
//
//   - Post-handler save, for a response writer without the hook
//     (httptest.ResponseRecorder, custom wrappers), right after the
//     handler returns.
//
// Without this middleware every ctx.Auth().Session(r).Put("k", v) and
// every Flash() write is silently lost because the framework never calls
// session.Save(w) on its own.
//
// Auto-installed by velocity.bootstrap() when the active scheme is a
// *SessionScheme; consumers do not normally need to wire this themselves.
func (g *SessionScheme) SessionMiddleware() router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			// Replace the request with one carrying a sessionHolder so
			// any scheme call inside the handler caches its session
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

			// Hand the response writer to the holder so scheme read paths
			// (User/Check) can emit Set-Cookie during remember-cookie
			// revival: rotate-on-use needs to deliver the replacement
			// remember cookie, and those methods only see the request.
			if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && holder != nil {
				holder.setResponseWriter(c.Response)
			}

			// Eagerly bind a session to the request so anonymous-but-
			// stateful concerns (CSRF token mint, flash bag, anything
			// that wants a stable per-visitor id) have something to bind
			// to before the handler runs. Without this the lazy
			// SessionScheme.getSession path means a handler that never
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

			// saved makes the save run at most once per request, so
			// the session never writes two Set-Cookie headers (one
			// fresh, one stale) whichever site invokes doSave.
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
			// WriteHeader/Write/Hijack commits the response, whether the
			// handler or the router's error boundary writes it, and
			// otherwise once the boundary is done with the request.
			h, hooked := c.Response.(preCommitHooker)
			if hooked {
				h.BeforeFirstWrite(doSave)
			}

			err := next(c)

			// A writer without the hook (test recorders, custom
			// wrappers) saves here. sync.Once ensures we never
			// double-save.
			if !hooked {
				doSave()
			}
			return err
		}
	}
}

// ensureSession is the eager-bootstrap helper used by SessionMiddleware.
// It triggers the scheme's normal getSession path, which loads an
// existing session from cookie OR mints a fresh one via
// auth.GetSessionFromRequest's store.Create("") fallback. The session
// is cached in the request-scoped sessionHolder so downstream
// getSession callers observe the same instance, and (for freshly
// minted ids) BaseSession sets modified=true so the post-handler
// doSave path writes the Set-Cookie even when the handler never
// touches the bag.
//
// Exists as a package-level var so test fixtures that stub scheme
// internals can override it, mirroring saveSessionFromMiddleware's
// seam.
var ensureSession = func(g *SessionScheme, r *http.Request) {
	_ = g.getSession(r)
}

// saveSessionFromMiddleware is a small indirection so tests can override
// the save path without reaching into router/http internals. It exists
// solely to keep SessionMiddleware ergonomic to unit-test alongside the
// store implementation it drives.
var saveSessionFromMiddleware = func(g *SessionScheme, w http.ResponseWriter, s auth.Session) error {
	if err := s.Save(w); err != nil {
		g.logWarn("velocity/auth: save-at-end middleware: session save failed", "session_id", s.ID(), "error", err)
		return err
	}
	return nil
}
