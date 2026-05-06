package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// Algorithm describes a deterministic MAC primitive used by Signer and
// Verifier. Implementations must be safe for concurrent use; the default
// HMAC-SHA256 implementation creates a fresh hash per call.
type Algorithm interface {
	// Name returns a stable identifier for the algorithm (e.g. "hmac-sha256").
	Name() string
	// Sign returns the raw MAC bytes for the given secret and payload. It
	// must be deterministic: identical inputs must produce identical output.
	Sign(secret, payload []byte) []byte
}

// hmacSHA256 implements Algorithm with HMAC-SHA256. It is the default
// algorithm used by Signer and Verifier.
type hmacSHA256 struct{}

// Name returns the algorithm identifier.
func (hmacSHA256) Name() string { return "hmac-sha256" }

// Sign returns the HMAC-SHA256 of payload keyed by secret.
func (hmacSHA256) Sign(secret, payload []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write(payload)
	return h.Sum(nil)
}

// HMACSHA256 is the default signing algorithm. It implements Algorithm and
// is safe for concurrent use.
var HMACSHA256 Algorithm = hmacSHA256{}

// Signer produces signature header values over a payload using a pluggable
// Algorithm and a shared Secret. Zero-value Signers are not usable; both
// Algorithm and Secret must be set (typically via NewSigner or struct
// literal).
type Signer struct {
	// Algorithm is the MAC primitive used to sign payloads. Defaults are
	// not auto-applied; callers should set this explicitly or use NewSigner.
	Algorithm Algorithm
	// Secret is the shared signing key. Must be non-empty.
	Secret []byte
	// Now overrides the timestamp source for tests. When nil, time.Now is used.
	Now func() time.Time
}

// NewSigner returns a Signer configured with HMAC-SHA256 and the given secret.
func NewSigner(secret []byte) *Signer {
	return &Signer{Algorithm: HMACSHA256, Secret: secret}
}

// now returns the configured time source or wall-clock time.
func (s *Signer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Sign returns the hex-encoded signature and the unix-second timestamp string
// produced over the payload framed as "<timestamp>.<payload>". The framing
// prevents an attacker from sliding a payload to a different timestamp while
// reusing the MAC. Returns an error if Algorithm or Secret is unset.
func (s *Signer) Sign(payload []byte) (signature, timestamp string, err error) {
	if s.Algorithm == nil {
		return "", "", ErrNoAlgorithm
	}
	if len(s.Secret) == 0 {
		return "", "", ErrMissingSecret
	}
	ts := strconv.FormatInt(s.now().Unix(), 10)
	mac := s.Algorithm.Sign(s.Secret, framed(ts, payload))
	return hex.EncodeToString(mac), ts, nil
}

// Header returns a fully-formed signature header value of the form
// "t=<unix>,v1=<hex>". The format mirrors Stripe's webhook signature scheme:
// the timestamp is part of the signed material via framing, so a verifier
// can reject stale or replayed deliveries without trusting the header alone.
func (s *Signer) Header(payload []byte) (string, error) {
	sig, ts, err := s.Sign(payload)
	if err != nil {
		return "", err
	}
	return "t=" + ts + ",v1=" + sig, nil
}

// framed builds the signed payload "<timestamp>.<payload>" without copying
// the payload more than necessary.
func framed(ts string, payload []byte) []byte {
	out := make([]byte, 0, len(ts)+1+len(payload))
	out = append(out, ts...)
	out = append(out, '.')
	out = append(out, payload...)
	return out
}
