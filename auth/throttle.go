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

// throttleKeySep is the byte used to separate the normalised identifier
// from the canonical client IP inside the SHA-256 input. ASCII Unit
// Separator (0x1F) is the standard control character ECMA-48 reserves
// for "logical record element separators inside opaque data", which is
// exactly the role it plays here. It is also explicitly forbidden in
// every canonical IP textual representation (net.ParseIP/net.SplitHostPort
// reject it) and is normalised out of any sane credential identifier
// (NFKC + ToLower passes it through unchanged, but it has no use in
// emails / usernames). Without a separator that cannot appear in either
// component, a crafted identifier like "victim<sep>198.51.100.1"
// collides with the throttle key for ("victim", "198.51.100.1") and
// lets the attacker poison or share a victim's rate-limit bucket.
const throttleKeySep = "\x1f"

// Throttle key prefixes for the three independent rate-limit dimensions.
// A single (identifier, IP) pair bucket alone leaves two attack shapes
// unthrottled: password spraying (one IP rotating identifiers, so every
// attempt lands in a fresh pair bucket) and distributed brute force (one
// identifier attacked from many IPs, same effect). The per-identifier
// and per-IP dimensions close both; throttlers apply an independent,
// higher cap to each dimension and deny when any cap is exceeded. See
// V2-04.
//
// The id/ip prefixes are checked with strings.HasPrefix by limit
// selectors, so ThrottleKeyPairPrefix (a prefix of both) must be
// matched last.
const (
	// ThrottleKeyPairPrefix prefixes the (identifier, IP) pair dimension.
	ThrottleKeyPairPrefix = "login:"
	// ThrottleKeyIdentifierPrefix prefixes the per-identifier dimension.
	ThrottleKeyIdentifierPrefix = "login:id:"
	// ThrottleKeyIPPrefix prefixes the per-client-IP dimension.
	ThrottleKeyIPPrefix = "login:ip:"
)

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

	return pairThrottleKey(ident, ip)
}

// pairThrottleKey hashes the (identifier, IP) pair dimension.
//
// throttleKeySep (ASCII Unit Separator 0x1f) cannot appear in any
// canonical IP textual representation and is normalised out of any
// sane credential identifier. Using it as the separator stops a
// crafted identifier "victim<sep>198.51.100.1" from colliding with
// the throttle key for ("victim", "198.51.100.1"). See M-46.
func pairThrottleKey(ident, ip string) string {
	sum := sha256.Sum256([]byte(ident + throttleKeySep + ip))
	return ThrottleKeyPairPrefix + hex.EncodeToString(sum[:8])
}

// ThrottleKeys derives every rate-limiting key consulted for a login
// attempt, one per throttle dimension:
//
//   - pair: identical to ThrottleKey, the (identifier, IP) bucket.
//     Always present.
//   - identifier: the normalised identifier alone, shared across all
//     source IPs. Caps distributed brute force against one account.
//     Omitted when the request carries no recognisable identifier
//     (an empty-identifier bucket would pool unrelated traffic).
//   - IP: the client IP alone, shared across all identifiers. Caps
//     password spraying from one source. Omitted when the client IP
//     cannot be resolved.
//
// Guards check Allow for every returned key, record failures against
// every key, and clear every key on success. The pair and IP dimensions
// deny before the credential check; the identifier dimension is
// verify-first: it denies only attempts whose credentials are wrong, so
// an attacker spraying a victim's identifier from many IPs cannot lock
// the account holder (who presents the correct password) out (the
// account-lockout DoS). Each dimension carries its prefix
// (ThrottleKeyPairPrefix / ThrottleKeyIdentifierPrefix /
// ThrottleKeyIPPrefix) so throttler implementations can apply
// per-dimension limits. Identifier normalisation, IP resolution, and
// the hashed/length-bounded output format match ThrottleKey; raw
// identifiers and IPs never appear in keys.
func ThrottleKeys(r *http.Request, credentials map[string]interface{}, trustedProxies []*net.IPNet) []string {
	ident := normaliseIdentifier(credentials)
	ip := clientip.ExtractString(r, trustedProxies)

	keys := make([]string, 0, 3)
	keys = append(keys, pairThrottleKey(ident, ip))
	if ident != "" {
		sum := sha256.Sum256([]byte(ident))
		keys = append(keys, ThrottleKeyIdentifierPrefix+hex.EncodeToString(sum[:8]))
	}
	if ip != "" {
		sum := sha256.Sum256([]byte(ip))
		keys = append(keys, ThrottleKeyIPPrefix+hex.EncodeToString(sum[:8]))
	}
	return keys
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
