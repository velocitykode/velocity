package auth

import (
	"errors"
	"net/http"

	"github.com/velocitykode/velocity/contract"
)

// ErrLoginThrottled is returned from guard Attempt() methods when the
// configured LoginThrottler rejects an attempt before credentials are checked.
var ErrLoginThrottled = errors.New("velocity/auth: too many login attempts")

// NoopLoginThrottler is the default LoginThrottler used when no throttler
// is explicitly configured. It allows every request and records nothing.
type NoopLoginThrottler struct{}

// Allow always returns true.
func (NoopLoginThrottler) Allow(*http.Request, string) bool { return true }

// RecordFailure is a no-op.
func (NoopLoginThrottler) RecordFailure(*http.Request, string) {}

// RecordSuccess is a no-op.
func (NoopLoginThrottler) RecordSuccess(*http.Request, string) {}

// compile-time guarantee the no-op implements the contract.
var _ contract.LoginThrottler = NoopLoginThrottler{}

// ThrottleKey derives the rate-limiting key used for a login attempt from the
// request and supplied credentials. The resulting key is "<identifier>|<ip>"
// where identifier is the first non-empty value among the common credential
// keys (email, username, name). When no identifier is found, only the IP is
// used. Exported so alternative guards can reuse it.
func ThrottleKey(r *http.Request, credentials map[string]interface{}) string {
	ident := ""
	for _, field := range []string{"email", "username", "name", "login"} {
		if v, ok := credentials[field]; ok {
			if s, ok := v.(string); ok && s != "" {
				ident = s
				break
			}
		}
	}
	ip := ""
	if r != nil {
		ip = r.RemoteAddr
	}
	if ident == "" {
		return ip
	}
	return ident + "|" + ip
}
