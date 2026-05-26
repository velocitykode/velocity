package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/clientip"
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

// maxIdentifierBytes caps the per-request identifier portion of the
// throttle key BEFORE hashing. Without a cap an attacker can submit
// a multi-MB `email` field, amplifying every entry in the rate-limit
// store. 254 bytes matches the RFC 5321 email-length ceiling so any
// legitimate identifier fits comfortably.
const maxIdentifierBytes = 254

// ThrottleKey derives the rate-limiting key used for a login attempt.
//
// The key is a stable, length-bounded SHA-256 hex digest over the
// normalised credential identifier and the originating client IP. The
// identifier is lowered, NFKC-normalised, and whitespace-trimmed so
// case-rotated submissions ("Victim@example.com" vs "VICTIM@...") and
// Unicode confusables hit the same bucket. The IP is resolved via
// internal/clientip.Extract so a load-balancer-fronted deployment
// uses the real client IP and an unproxied deployment strips the
// ephemeral TCP port.
//
// trustedProxies is the list of proxy networks whose forwarded headers
// may be honoured; pass nil to disable XFF/Forwarded resolution (secure
// default). At boot the framework parses Config.TrustedProxies and
// propagates the result to every guard via SetTrustedProxies.
//
// The hex output is prefixed with "login:" so cache-backend operators
// can distinguish login-throttle entries from other rate-limit keys.
// Output is exactly 22 bytes: "login:" + 16 hex chars (64 bits of the
// SHA-256 digest), well under every cache backend's key-length cap.
func ThrottleKey(r *http.Request, credentials map[string]interface{}, trustedProxies []*net.IPNet) string {
	ident := normaliseIdentifier(credentials)

	// Resolve the client IP. Extract returns "" only when RemoteAddr is
	// unparseable; in that case the IP component contributes nothing and
	// the key is solely identifier-derived. That degrades gracefully
	// (still throttles by identifier) without falling through to a
	// unique-per-connection key.
	ip := clientip.ExtractString(r, trustedProxies)

	// Use NUL as separator so neither ident nor ip can collide with
	// another (ident, ip) pair via a "|"-bearing identifier (e.g.
	// `alice|10.0.0.5` impersonating `alice` from 10.0.0.5). Hashing
	// further removes the separator as an attack surface.
	sum := sha256.Sum256([]byte(ident + "\x00" + ip))
	return "login:" + hex.EncodeToString(sum[:8])
}

// normaliseIdentifier extracts the first non-empty value among the
// common credential keys (email, username, name, login) and applies
// the same case/whitespace/Unicode folding the UserProvider must use
// when looking up users. Without normalisation
// "Victim@example.com" and "VICTIM@example.com" hash to distinct
// throttle keys but resolve to the same account.
//
// The result is also capped at maxIdentifierBytes so an attacker
// cannot multiply the cost of every limiter entry by submitting a
// multi-MB credential field.
func normaliseIdentifier(credentials map[string]interface{}) string {
	for _, field := range []string{"email", "username", "name", "login"} {
		v, ok := credentials[field]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		s = norm.NFKC.String(s)
		s = strings.ToLower(s)
		if len(s) > maxIdentifierBytes {
			s = s[:maxIdentifierBytes]
		}
		return s
	}
	return ""
}
