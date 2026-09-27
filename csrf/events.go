package csrf

import (
	"net/http"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventmeta"
)

// SessionMissing is dispatched when an unsafe request reaches the CSRF
// middleware without a session the SessionIDResolver accepts. The request
// is rejected (419): a CSRF token is only ever valid for a real session.
// It fires whether or not the request carried a token, so every
// session-less unsafe request is reported once. Frequent occurrences
// usually mean the session middleware does not run upstream of the CSRF
// middleware, the session cookie name does not match
// Config.SessionCookieName, or the session store rejected the cookie
// (expired, revoked or undecryptable).
type SessionMissing struct {
	contract.EventMeta
	Path   string
	Method string
}

// Name returns the event name.
func (e *SessionMissing) Name() string {
	return "csrf.session.missed"
}

// dispatchSessionMissing dispatches SessionMissing for r. The event is
// built only when a dispatcher is installed.
func (c *CSRF) dispatchSessionMissing(r *http.Request) {
	if !c.hasEventDispatcher() {
		return
	}
	c.dispatchEvent(r.Context(), &SessionMissing{
		EventMeta: eventmeta.Current(r.Context()),
		Path:      r.URL.Path,
		Method:    r.Method,
	})
}
