package auth

import (
	"context"
	"time"
)

// AuthenticationFailed is dispatched when an authentication attempt fails
type AuthenticationFailed struct {
	Context context.Context
	Guard   string
	Reason  string
	At      time.Time
}

// Name returns the event name
func (e *AuthenticationFailed) Name() string {
	return "auth.authentication.failed"
}
