package csrf

import (
	"context"
	"time"
)

// SessionFallback is dispatched whenever CSRF cannot locate a session cookie
// on an unsafe request and falls back to generating an ephemeral session ID.
// Frequent occurrences of this event usually indicate that the session middleware
// is not running upstream of the CSRF middleware, or that the session cookie
// name does not match Config.SessionCookieName.
type SessionFallback struct {
	Context context.Context
	Path    string
	Method  string
	At      time.Time
}

// Name returns the event name.
func (e *SessionFallback) Name() string {
	return "csrf.session_fallback"
}
