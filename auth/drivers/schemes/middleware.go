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
// The save is the one place the framework persists a session: Login,
// Logout, the intended-URL stash and resolver, and view flashes only
// mutate the request's session, so a request emits at most one session
// Set-Cookie. After a successful save the seam writes the cookies Login
// bound to the saved session id (XSRF-TOKEN, then remember), so a client
// never holds either for a session that was not persisted. Login and
// Logout called outside this middleware (a plain net/http handler, a
// script, a test) are their own save scope and commit through the same
// seam body before returning.
//
// velocity.New installs it on the app router whenever the default scheme
// is a *SessionScheme (see SessionMiddlewareFor); consumers do not
// normally need to wire this themselves.
func (g *SessionScheme) SessionMiddleware() router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			return g.serveWithSession(c, next)
		}
	}
}

// SessionMiddlewareFor returns a middleware that runs each request through
// the SessionMiddleware of the scheme current returns at request time, or
// straight to the handler when current returns nil. It lets the
// application install the save seam once, when the router is built, while
// the scheme it serves follows later changes to the default scheme.
func SessionMiddlewareFor(current func() *SessionScheme) router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			g := current()
			if g == nil {
				return next(c)
			}
			return g.serveWithSession(c, next)
		}
	}
}

// serveWithSession runs next inside the save seam; see SessionMiddleware.
func (g *SessionScheme) serveWithSession(c *router.Context, next router.HandlerFunc) error {
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
			if holder, ok := c.Request.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && holder != nil {
				_ = commitSession(g, c.Response, holder)
			}
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

// commitSession is the save seam's body and the only framework code that
// saves a session: it saves holder's session to w when it changed, then
// runs the cookie writes queued behind the save. A failed save drops the
// queued writes, which are bound to the session id the save did not
// persist, and is returned.
func commitSession(g *SessionScheme, w http.ResponseWriter, holder *sessionHolder) error {
	queued := holder.takeAfterSave()
	session := holder.getSession()
	if session == nil {
		return nil
	}
	// Skip the save when no mutation occurred. The modifiedSession
	// capability covers *auth.BaseSession and the cookie store's
	// wrapper; sessions that do not expose the capability fall through
	// to an unconditional Save (cheaper than reflection, and
	// CookieStore.Save itself short-circuits on !IsModified() too).
	if ms, ok := session.(modifiedSession); !ok || ms.IsModified() || ms.IsDestroyed() {
		if err := saveSessionFromMiddleware(g, w, session); err != nil {
			if len(queued) > 0 {
				g.logWarn("velocity/auth: save-at-end middleware: cookies bound to the unsaved session dropped", "session_id", session.ID(), "count", len(queued))
			}
			return err
		}
	}
	for _, fn := range queued {
		fn(w)
	}
	return nil
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
