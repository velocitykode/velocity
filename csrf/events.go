package csrf

import (
	"context"
	"time"
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
	Context context.Context
	Path    string
	Method  string
	At      time.Time
}

// Name returns the event name.
func (e *SessionMissing) Name() string {
	return "csrf.session_missing"
}
