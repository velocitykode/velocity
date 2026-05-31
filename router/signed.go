package router

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"
)

// Signed URL primitives.
//
// SignedURL mints a URL that carries an "expires" timestamp and a
// hex-encoded HMAC-SHA256 "signature" query parameter. ValidateSignature
// (and the SignedMiddleware that wraps it) verifies the signature in
// constant time, rejects expired URLs, and rejects any tampering of the
// path, expires value, or auxiliary query parameters. The primitives
// (SignedURL, ValidateSignature, and the `signed` middleware) give
// consumer apps a ready-made surface for common use cases (password
// reset links, email verification, unsubscribe URLs, time-limited
// download links).
//
// The signing key is derived from APP_KEY via HKDF-SHA256 with the info
// label "velocity-signed-url-v1" so that signed-URL signatures never
// collide with other HMAC subsystems that share the same master key
// (queue payload signatures, maintenance bypass MACs, session cookies).
// Each subsystem stays independently rotatable; a forged signed URL on
// one subkey grants nothing on another.

const (
	// signedURLExpiresParam is the canonical name for the absolute
	// Unix-second expiry timestamp embedded in signed URLs. Lower-case
	// by convention.
	signedURLExpiresParam = "expires"
	// signedURLSignatureParam is the canonical name for the hex-encoded
	// HMAC-SHA256 tag. Lower-case by convention.
	signedURLSignatureParam = "signature"
	// signedURLHKDFInfo separates this subsystem's HMAC key from every
	// other HKDF-derived key in the framework. Version suffix lets us
	// rotate the cipher in a future release without breaking existing
	// URLs (a v2 verifier would still accept v1 signatures during the
	// rotation window).
	signedURLHKDFInfo = "velocity-signed-url-v1"
	// signedURLKeySize is the derived subkey length. 32 bytes matches
	// HMAC-SHA256's block size and keeps the key indistinguishable from
	// random to anyone without APP_KEY.
	signedURLKeySize = 32
)

// Signed URL error sentinels. Callers branch on errors.Is to distinguish
// the rejection cause (e.g. issue a refresh link on ErrSignatureExpired
// versus a tamper-tripwire alert on ErrSignatureInvalid). The middleware
// collapses every sentinel into a 403 *HTTPError so clients see a
// uniform response; the underlying cause is preserved via Unwrap for
// server-side logging.
var (
	// ErrSignatureMissing is returned when a request carries no
	// "signature" query parameter. Distinct from ErrSignatureInvalid
	// so callers can decide whether to redirect to a "resend link"
	// page versus a hard 403.
	ErrSignatureMissing = errors.New("velocity/router: signed URL signature missing")
	// ErrSignatureInvalid is returned when the supplied signature does
	// not match the recomputed HMAC, including when the path, query,
	// or expires value has been tampered with.
	ErrSignatureInvalid = errors.New("velocity/router: signed URL signature invalid")
	// ErrSignatureExpired is returned when the URL's "expires" param
	// is in the past relative to time.Now(). Verified BEFORE the HMAC
	// compare so an attacker cannot probe signature validity for an
	// expired URL by observing timing.
	ErrSignatureExpired = errors.New("velocity/router: signed URL expired")
	// ErrSignedURLKeyMissing is returned by SignedURL / ValidateSignature
	// when the router was constructed without an APP_KEY (e.g. testing
	// env). It is intentionally a separate sentinel from
	// ErrSignatureInvalid so a misconfigured deployment does not look
	// like a tamper attempt in operator logs.
	ErrSignedURLKeyMissing = errors.New("velocity/router: signed URL signing key not configured")
)

// signedURLKey holds the HKDF-derived subkey used for signing and
// verifying URLs. The mutex covers Set/get so a future runtime rotation
// (e.g. APP_KEY reload) is race-free. Read path is RLock so middleware
// stays cheap on the hot path.
type signedURLKey struct {
	mu  sync.RWMutex
	key []byte
}

func (k *signedURLKey) set(b []byte) {
	k.mu.Lock()
	if len(b) == 0 {
		k.key = nil
	} else {
		// Defensive copy: the caller may reuse the byte slice for
		// other HKDF-derived material.
		k.key = append([]byte(nil), b...)
	}
	k.mu.Unlock()
}

func (k *signedURLKey) get() []byte {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.key
}

// SetSignedURLKey installs the HMAC key used by SignedURL and
// ValidateSignature. The framework derives this key from APP_KEY via
// HKDF-SHA256 in velocity.New() and calls this setter once before
// Serve(). Pass nil/empty to disable signed-URL minting and verification
// (e.g. in environments without an APP_KEY).
//
// Safe to call from any goroutine; the underlying slot is mutex-guarded
// so a future hot-rotation path can swap the key without restarting the
// process.
func (r *VelocityRouterV2) SetSignedURLKey(key []byte) {
	r.signedURLKey.set(key)
}

// DeriveSignedURLKey produces the signed-URL HMAC subkey from the
// supplied master key (typically APP_KEY) using HKDF-SHA256 with the
// signed-URL-specific info label. Returns ErrSignedURLKeyMissing for an
// empty master so callers can branch on the missing-key case explicitly.
// The salt is intentionally empty (the info label provides the domain
// separation, mirroring queue/signing.go's pattern).
func DeriveSignedURLKey(appKey []byte) ([]byte, error) {
	if len(appKey) == 0 {
		return nil, ErrSignedURLKeyMissing
	}
	r := hkdf.New(sha256.New, appKey, nil, []byte(signedURLHKDFInfo))
	out := make([]byte, signedURLKeySize)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("velocity/router: HKDF derivation failed: %w", err)
	}
	return out, nil
}

// SignedURL returns a URL for the named route with `expires` and
// `signature` query parameters appended. params populates route
// placeholders (e.g. {id}). extraQuery is added to the URL as-is and
// is covered by the signature so a third party cannot strip or rewrite
// it. expiresAt is the absolute UTC instant at which the URL stops
// being valid; pass time.Time{} (zero) to mint a URL with no expiry.
//
// The signature commits to the canonical form: path + sorted query
// pairs (including the `expires` parameter, excluding `signature`).
// Sorting is deterministic so a client that reorders query parameters
// in transit (or a CDN that normalises them) still verifies. Encoding
// follows url.Values.Encode (RFC 3986 percent-encoding); both sides
// use the same encoder so round-tripping is exact.
//
// Returns ErrSignedURLKeyMissing if SetSignedURLKey was never called or
// was called with nil. Returns RouteNotFoundError if name was never
// registered or if routes have not yet been committed (first request).
func (r *VelocityRouterV2) SignedURL(name string, params map[string]string, extraQuery url.Values, expiresAt time.Time) (string, error) {
	path, err := r.RouteURL(name, params)
	if err != nil {
		return "", err
	}
	return r.signPath(path, extraQuery, expiresAt)
}

// TemporarySignedURL is the ttl-relative convenience wrapper around
// SignedURL. ttl <= 0 mints a URL that is already expired; callers
// should treat this as a programming error in tests but the framework
// does not reject it so test fixtures can produce known-expired URLs
// for negative-path assertions.
func (r *VelocityRouterV2) TemporarySignedURL(name string, params map[string]string, extraQuery url.Values, ttl time.Duration) (string, error) {
	return r.SignedURL(name, params, extraQuery, time.Now().Add(ttl))
}

// signPath builds the signature over a raw path (already param-expanded)
// plus any extraQuery, appends `expires` (if non-zero) and `signature`,
// and returns the final URL string. Exposed to tests via a thin wrapper
// so they can exercise canonicalisation without registering a route.
func (r *VelocityRouterV2) signPath(path string, extraQuery url.Values, expiresAt time.Time) (string, error) {
	key := r.signedURLKey.get()
	if len(key) == 0 {
		return "", ErrSignedURLKeyMissing
	}

	// Copy extraQuery so the caller's map is not mutated when we add
	// `expires`. nil-safe.
	q := url.Values{}
	for k, vs := range extraQuery {
		// Reject reserved parameter names early so a caller cannot
		// accidentally smuggle a fake signature past the verifier.
		if k == signedURLSignatureParam || k == signedURLExpiresParam {
			return "", fmt.Errorf("velocity/router: query parameter %q is reserved by SignedURL", k)
		}
		// Defensive copy of the value slice; url.Values.Encode reads
		// these directly.
		q[k] = append([]string(nil), vs...)
	}

	if !expiresAt.IsZero() {
		q.Set(signedURLExpiresParam, strconv.FormatInt(expiresAt.Unix(), 10))
	}

	signature := computeSignedURLSignature(key, path, q)
	q.Set(signedURLSignatureParam, signature)

	if len(q) == 0 {
		return path, nil
	}
	return path + "?" + q.Encode(), nil
}

// HasValidSignature returns true iff the request's URL carries a
// signature that matches the canonical HMAC and (if present) the
// `expires` timestamp is still in the future. It never panics on a
// missing key; instead it returns false so the caller is responsible
// for treating "key unset" as "no valid signature".
func (r *VelocityRouterV2) HasValidSignature(req *http.Request) bool {
	return r.ValidateSignature(req) == nil
}

// ValidateSignature is the error-returning sibling of HasValidSignature.
// Returns one of: nil (valid), ErrSignedURLKeyMissing, ErrSignatureMissing,
// ErrSignatureInvalid, or ErrSignatureExpired. The expiry check runs
// BEFORE the HMAC compare so an expired URL is always reported as
// expired rather than invalid, removing a timing channel that would let
// an attacker distinguish "tampered, but expired" from "tampered, not
// yet expired".
func (r *VelocityRouterV2) ValidateSignature(req *http.Request) error {
	key := r.signedURLKey.get()
	if len(key) == 0 {
		return ErrSignedURLKeyMissing
	}
	if req == nil || req.URL == nil {
		return ErrSignatureInvalid
	}

	// Work on a copy of req.URL.Query so we can strip `signature`
	// before recomputing the MAC.
	q := req.URL.Query()
	supplied := q.Get(signedURLSignatureParam)
	if supplied == "" {
		return ErrSignatureMissing
	}
	q.Del(signedURLSignatureParam)

	// Reject malformed `expires` BEFORE any timing-sensitive work: a
	// non-integer expires cannot be honoured under any signature, and
	// we want the error class to match the operator-visible failure
	// mode (tampering / typo) not "expired".
	if exp := q.Get(signedURLExpiresParam); exp != "" {
		expUnix, err := strconv.ParseInt(exp, 10, 64)
		if err != nil {
			return ErrSignatureInvalid
		}
		if time.Now().Unix() >= expUnix {
			return ErrSignatureExpired
		}
	}

	expected := computeSignedURLSignature(key, req.URL.Path, q)

	// Constant-time compare per CLAUDE.md rule 7. ConstantTimeCompare
	// returns 0 on length mismatch which we treat as a tamper signal,
	// not a separate error class.
	if subtle.ConstantTimeCompare([]byte(expected), []byte(supplied)) != 1 {
		return ErrSignatureInvalid
	}
	return nil
}

// SignedMiddleware returns a MiddlewareFunc that rejects requests
// failing ValidateSignature with a 403 *HTTPError. The underlying
// signature error is preserved via Internal so server-side logs see
// the real cause; clients see a generic "Forbidden" body per CLAUDE.md
// rule 6 (no error leakage).
//
// Fails closed when the router has no signed-URL key configured: every
// request through the middleware is rejected with 403 wrapping
// ErrSignedURLKeyMissing. A route the developer chose to wrap in
// SignedMiddleware is opt-in to signing enforcement; if the key was
// never derived (e.g. APP_KEY left empty under a custom CRYPTO_KEY
// deployment) the route is broken at boot, not silently downgraded to
// "unsigned route". This is the M-16 fix: the previous fail-open path
// could turn a protected signed route into an unauthenticated route
// whenever APP_KEY was unset. Operators see ErrSignedURLKeyMissing in
// the access log the first time the route is hit; clients see the same
// 403 as a tampered-signature attempt.
//
// The boot-time guard in velocity.New() refuses to start outside
// APP_ENV=testing/development without APP_KEY, so production
// deployments cannot hit this branch unless they explicitly opt out.
func (r *VelocityRouterV2) SignedMiddleware() MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			if len(r.signedURLKey.get()) == 0 {
				httpErr := NewHTTPError(http.StatusForbidden)
				httpErr.Internal = ErrSignedURLKeyMissing
				return httpErr
			}
			if err := r.ValidateSignature(c.Request); err != nil {
				httpErr := NewHTTPError(http.StatusForbidden)
				httpErr.Internal = err
				return httpErr
			}
			return next(c)
		}
	}
}

// computeSignedURLSignature returns the hex-encoded HMAC-SHA256 over
// the canonical "path?sorted_query" string. The query parameters are
// sorted lexicographically by key, and within a key the values keep
// the order url.Values stores them in (insertion order: the caller's
// SignedURL never inserts duplicates so this is deterministic). The
// signature parameter itself MUST be absent from q before calling.
func computeSignedURLSignature(key []byte, path string, q url.Values) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(path))
	if len(q) > 0 {
		mac.Write([]byte{'?'})
		mac.Write([]byte(canonicalQuery(q)))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// canonicalQuery returns a deterministic encoding of q with keys sorted
// lexicographically. Equivalent to url.Values.Encode (which already
// sorts by key), but we re-implement it so a future url package change
// cannot silently break signature compatibility for URLs minted under
// the old encoder.
func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, k := range keys {
		escKey := url.QueryEscape(k)
		for j, v := range q[k] {
			if i > 0 || j > 0 {
				b.WriteByte('&')
			}
			b.WriteString(escKey)
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(v))
		}
	}
	return b.String()
}
