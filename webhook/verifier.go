package webhook

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// NonceStore is an optional dependency on Verifier that records signatures
// (or any caller-derived nonce) so a second delivery of the same signed
// payload can be rejected. Implementations must be safe for concurrent use.
//
// CheckAndMark must be atomic: a single concurrent caller observing
// alreadySeen=false implies that no other caller will observe the same
// nonce as alreadySeen=false within the ttl window. A naive
// "Seen-then-Mark" pair is insufficient because it allows two concurrent
// verifications of the same payload to both succeed (TOCTOU replay).
type NonceStore interface {
	// CheckAndMark atomically reports whether nonce was already present
	// (and unexpired) and, if not, records it with the supplied ttl.
	CheckAndMark(ctx context.Context, nonce string, ttl time.Duration) (alreadySeen bool, err error)
}

// Verifier validates signature header values produced by Signer.
//
// A Verifier with no NonceStore performs MAC + timestamp checks only.
// Supplying a NonceStore enables exactly-once semantics within the
// Tolerance window (the nonce TTL is automatically set to Tolerance so
// expired signatures cannot be replayed).
type Verifier struct {
	// Algorithm must match the Signer's Algorithm. Defaults are not
	// auto-applied; set this to HMACSHA256 explicitly when constructing.
	Algorithm Algorithm
	// Secret is the shared signing key. Must be non-empty.
	Secret []byte
	// Tolerance is the maximum age of a signature header (positive duration).
	// Signatures whose timestamp is more than Tolerance behind or ahead of
	// the current clock are rejected with ErrTimestampOutOfTolerance.
	// Zero (the zero value) means the default of 5 minutes; the check
	// cannot be disabled by zeroing this field. Set DisableTimestampCheck
	// for the rare legitimate opt-out.
	Tolerance time.Duration
	// DisableTimestampCheck skips signature freshness validation entirely.
	//
	// WARNING: with this set, a correctly signed payload verifies forever.
	// Without a NonceStore in Nonces the replay window is unbounded: any
	// captured delivery can be re-sent indefinitely. Only set this when an
	// upstream provider cannot produce timestamps AND replay is mitigated
	// elsewhere (ideally by also setting Nonces).
	DisableTimestampCheck bool
	// Nonces, when non-nil, enables replay rejection. The hex signature is
	// used as the nonce; the TTL written to the store equals Tolerance
	// (or 5 minutes when Tolerance is zero). Note the TTL stays finite even
	// with DisableTimestampCheck set, so a replay arriving after the nonce
	// expires is accepted again.
	Nonces NonceStore
	// Now overrides the timestamp source for tests. When nil, time.Now is used.
	Now func() time.Time
}

// NewVerifier returns a Verifier configured with HMAC-SHA256 and the given
// secret. Tolerance defaults to 5 minutes. Set Nonces on the returned struct
// to enable replay protection.
func NewVerifier(secret []byte) *Verifier {
	return &Verifier{
		Algorithm: HMACSHA256,
		Secret:    secret,
		Tolerance: defaultTolerance,
	}
}

// defaultTolerance is the freshness window applied when Tolerance is left
// at its zero value, so a zero-value Verifier still rejects stale signatures.
const defaultTolerance = 5 * time.Minute

// tolerance returns the effective freshness window: Tolerance when positive,
// defaultTolerance otherwise.
func (v *Verifier) tolerance() time.Duration {
	if v.Tolerance > 0 {
		return v.Tolerance
	}
	return defaultTolerance
}

// now returns the configured time source or wall-clock time.
func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// Verify parses the header, recomputes the MAC over "<timestamp>.<payload>",
// compares in constant time, and (optionally) atomically records the nonce
// so a second call with the same header returns ErrReplay.
//
// Returns nil on success. On failure returns one of: ErrMalformedHeader,
// ErrMissingSecret, ErrNoAlgorithm, ErrTimestampOutOfTolerance,
// ErrSignatureMismatch, ErrReplay, or a NonceStore error from CheckAndMark.
func (v *Verifier) Verify(payload []byte, header string) error {
	return v.VerifyContext(context.Background(), payload, header)
}

// VerifyContext is Verify with a caller-supplied context that is forwarded
// to the NonceStore.
func (v *Verifier) VerifyContext(ctx context.Context, payload []byte, header string) error {
	if v.Algorithm == nil {
		return ErrNoAlgorithm
	}
	if len(v.Secret) == 0 {
		return ErrMissingSecret
	}

	ts, sigHex, err := parseHeader(header)
	if err != nil {
		return err
	}

	if !v.DisableTimestampCheck {
		signedAt := time.Unix(ts, 0)
		delta := v.now().Sub(signedAt)
		if delta < 0 {
			delta = -delta
		}
		if delta > v.tolerance() {
			return ErrTimestampOutOfTolerance
		}
	}

	supplied, err := hex.DecodeString(sigHex)
	if err != nil {
		return ErrMalformedHeader
	}

	expected := v.Algorithm.Sign(v.Secret, framed(strconv.FormatInt(ts, 10), payload))
	if subtle.ConstantTimeCompare(expected, supplied) != 1 {
		return ErrSignatureMismatch
	}

	if v.Nonces != nil {
		seen, err := v.Nonces.CheckAndMark(ctx, sigHex, v.tolerance())
		if err != nil {
			return err
		}
		if seen {
			return ErrReplay
		}
	}

	return nil
}

// parseHeader extracts the unix timestamp and hex signature from a header
// value of the form "t=<unix>,v1=<hex>". The order of fields is not
// significant; unknown fields are ignored. Returns ErrMalformedHeader on
// any structural problem.
func parseHeader(header string) (ts int64, sig string, err error) {
	if header == "" {
		return 0, "", ErrMalformedHeader
	}
	var (
		haveTs, haveSig bool
	)
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		eq := strings.IndexByte(part, '=')
		if eq <= 0 || eq == len(part)-1 {
			return 0, "", ErrMalformedHeader
		}
		key := part[:eq]
		val := part[eq+1:]
		switch key {
		case "t":
			n, perr := strconv.ParseInt(val, 10, 64)
			if perr != nil {
				return 0, "", ErrMalformedHeader
			}
			ts = n
			haveTs = true
		case "v1":
			// Sanity-check: v1 must be even-length hex.
			if len(val) == 0 || len(val)%2 != 0 {
				return 0, "", ErrMalformedHeader
			}
			sig = val
			haveSig = true
		}
	}
	if !haveTs || !haveSig {
		return 0, "", ErrMalformedHeader
	}
	return ts, sig, nil
}
