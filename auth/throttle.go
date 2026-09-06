package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/clientip"
)

// ErrLoginThrottled is returned from scheme Attempt() methods when the
// configured LoginThrottler rejects an attempt before credentials are checked.
var ErrLoginThrottled = errors.New("velocity/auth: too many login attempts")

// ErrLoginChallengeRequired is returned from scheme Attempt() when the
// identifier is over cap, another credential trial for it already holds
// the admission slot, and a LoginChallenge is configured that this
// request did not pass. It wraps ErrLoginThrottled (errors.Is holds) so
// existing throttle handling keeps working; handlers that render a
// challenge (CAPTCHA, email code) match it specifically. Without a
// configured challenge the same condition yields ErrLoginThrottled.
var ErrLoginChallengeRequired = fmt.Errorf("%w: challenge required", ErrLoginThrottled)

// LoginChallenge reports whether r carries a passed interactive
// challenge (a verified CAPTCHA token, an out-of-band code). When one
// is installed via Manager.SetLoginChallenge, an over-cap identifier
// attempt that passes it skips the progressive delay and the admission
// slot: the challenge has already proven the caller is not an automated
// guesser. It never bypasses the pair or IP caps, which deny before the
// credential check regardless. The function must be safe for concurrent
// use and must not consume the request body without restoring it.
type LoginChallenge func(r *http.Request) bool

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

// Progressive-delay defaults for the identifier throttle dimension.
//
// The identifier bucket is verify-first (an over-cap bucket still runs
// the credential check so the account holder is never locked out), so
// once the pair and IP buckets are rotated by distributed or spoofed
// source addresses it bounds nothing by itself. The delay is the
// control that does: every attempt against an over-cap identifier pays
// it, right or wrong, and it doubles with each further failure up to
// the ceiling. A delay, unlike a lockout, degrades the account holder's
// login by at most the ceiling while an attack is in progress.
const (
	// DefaultIdentifierDelay is the delay paid by the first attempt
	// past the identifier cap, and the fixed delay applied when the
	// installed LoginThrottler does not implement contract.LoginDelayer.
	DefaultIdentifierDelay = 1 * time.Second
	// DefaultIdentifierDelayMax caps the progressive delay.
	DefaultIdentifierDelayMax = 30 * time.Second
)

// ProgressiveDelay returns the bounded exponential delay for the
// excess-th attempt past a throttle cap: base for excess 1, doubling
// for each further attempt, never exceeding ceiling. Returns 0 when excess
// < 1 or base <= 0; a ceiling <= 0 falls back to DefaultIdentifierDelayMax.
// Shared by the built-in cache throttler and available to custom
// contract.LoginDelayer implementations.
func ProgressiveDelay(excess int64, base, ceiling time.Duration) time.Duration {
	if excess < 1 || base <= 0 {
		return 0
	}
	if ceiling <= 0 {
		ceiling = DefaultIdentifierDelayMax
	}
	if base >= ceiling {
		return ceiling
	}
	// Doubling more than ~40 times overflows time.Duration; anything
	// past the point where base<<shift exceeds the ceiling clamps to it.
	const maxShift = 40
	shift := excess - 1
	if shift > maxShift {
		return ceiling
	}
	d := base << uint(shift)
	if d <= 0 || d > ceiling {
		return ceiling
	}
	return d
}

// LocalLoginAdmitter is the per-process fallback contract.LoginAdmitter
// used when the installed LoginThrottler does not implement the
// capability. It holds one slot per key in memory, so it bounds
// aggregate trials per app instance only; a multi-instance deployment
// gets K slots for K instances unless the throttler provides a
// store-backed Admit. The zero value is ready to use.
type LocalLoginAdmitter struct {
	mu    sync.Mutex
	slots map[string]time.Time
	now   func() time.Time
}

// localAdmitterSweepAt bounds the slot map: once it holds this many
// entries every Admit first drops the expired ones. Only over-cap
// identifiers ever get an entry and each expires after its hold, so
// the map stays small in practice; the sweep guards the pathological
// case of an attacker rotating identifiers past their caps.
const localAdmitterSweepAt = 1024

// Admit implements contract.LoginAdmitter for a single process.
func (a *LocalLoginAdmitter) Admit(_ *http.Request, key string, hold time.Duration) bool {
	if a == nil || hold <= 0 {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	if a.now != nil {
		now = a.now()
	}
	if a.slots == nil {
		a.slots = make(map[string]time.Time)
	}
	if len(a.slots) >= localAdmitterSweepAt {
		for k, until := range a.slots {
			if !until.After(now) {
				delete(a.slots, k)
			}
		}
	}
	if until, held := a.slots[key]; held && until.After(now) {
		return false
	}
	a.slots[key] = now.Add(hold)
	return true
}

// Release drops the slot for key so the next Admit succeeds
// immediately. Schemes call it after a successful login clears the
// identifier bucket.
func (a *LocalLoginAdmitter) Release(key string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	delete(a.slots, key)
	a.mu.Unlock()
}

// AdmitIdentifierTrial claims the credential-trial slot for an over-cap
// identifier key: through the throttler's own contract.LoginAdmitter
// when it implements one (store-backed, spans instances), else through
// fallback (per process). A nil fallback with a throttler lacking the
// capability admits unconditionally.
func AdmitIdentifierTrial(throttler contract.LoginThrottler, fallback *LocalLoginAdmitter, r *http.Request, key string, hold time.Duration) bool {
	if hold <= 0 {
		return true
	}
	if a, ok := throttler.(contract.LoginAdmitter); ok {
		return a.Admit(r, key, hold)
	}
	return fallback.Admit(r, key, hold)
}

// IdentifierDelay returns the delay an attempt against an over-cap
// identifier key must pay: the throttler's own contract.LoginDelayer
// answer when it implements one, DefaultIdentifierDelay otherwise.
// Negative answers are clamped to 0.
func IdentifierDelay(throttler contract.LoginThrottler, r *http.Request, key string) time.Duration {
	if throttler == nil {
		return 0
	}
	if d, ok := throttler.(contract.LoginDelayer); ok {
		delay := d.Delay(r, key)
		if delay < 0 {
			return 0
		}
		return delay
	}
	return DefaultIdentifierDelay
}

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
// propagates the result to every scheme via SetTrustedProxies.
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
// Schemes check Allow for every returned key, record failures against
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
// the same case/whitespace/Unicode folding the UserStore must use
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
