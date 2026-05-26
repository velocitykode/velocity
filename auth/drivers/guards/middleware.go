package guards

import (
	"net/http"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/router"
)

// SessionMiddleware returns a router.MiddlewareFunc that enables Laravel
// StartSession-equivalent semantics: every request is given a sessionHolder
// (via WithSessionContext) so SessionGuard.getSession can cache the
// resolved session for the lifetime of the request; AFTER the handler
// returns, the holder is consulted and, if a session was touched and
// mutated, it is saved to the response writer.
//
// Without this middleware every ctx.Auth().Session(r).Put("k", v) and
// every Flash() write is silently lost because the framework never calls
// session.Save(w) on its own. Guards' Login/Logout call Save inline, but
// generic in-handler session mutations rely on a save-at-end hook, which
// is precisely what this middleware provides.
//
// The save call goes through CookieStore.Save which is a no-op when the
// session is not modified, so the cost on the steady-state read path is
// a couple of map lookups and an interface check.
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

			err := next(c)

			// Post-handler: if any code path inside the handler
			// resolved the session and mutated it, persist now.
			session := sessionFromHolder(c.Request)
			if session == nil {
				return err
			}

			// Honour the cheap "no work to do" path: when the session
			// is neither modified nor destroyed, skip the Save() call
			// entirely. This avoids touching the response writer on
			// the common read-only path.
			//
			// Destroyed sessions are saved (Save writes the cookie
			// delete header). Logout already calls Save itself, but
			// running Save twice on a destroyed session is harmless:
			// CookieStore.Save short-circuits after writing the
			// delete header.
			if ms, ok := session.(modifiedSession); ok {
				if !ms.IsModified() && !ms.IsDestroyed() {
					return err
				}
			}

			// Save errors are swallowed because the handler may already
			// have committed a response body / status. Surfacing the
			// error here would either no-op (if headers are flushed)
			// or replace a successful handler return with a session
			// persistence failure, both worse than silent best-effort.
			//
			// A logger hook on the guard is the right place to report;
			// SessionGuard.logWarn is already wired through Manager
			// for store-side errors.
			_ = saveSessionFromMiddleware(g, c.Response, session)
			return err
		}
	}
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
