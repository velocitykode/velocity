package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"net/url"
	"strings"
	"time"
)

// TOTP errors. These are intentionally generic so callers can decide how
// (and whether) to surface them; never leak the underlying reason to a
// client because a code mismatch and a malformed input are both "invalid".
var (
	// ErrInvalidTOTPSecret is returned when a supplied base32 secret cannot
	// be decoded.
	ErrInvalidTOTPSecret = errors.New("invalid totp secret")
	// ErrInvalidRecoveryCount is returned when a non-positive count is
	// supplied to GenerateRecoveryCodes.
	ErrInvalidRecoveryCount = errors.New("invalid recovery code count")
)

// totpSecretBytes is the recommended secret size for HMAC-SHA1 TOTP per
// RFC 6238 section 5.1: 160 bits / 20 bytes.
const totpSecretBytes = 20

// recoveryCodeAlphabet excludes visually ambiguous characters (0/O, 1/I/l)
// so users transcribing codes from a printout do not confuse them. The
// alphabet is 31 characters; we read uniform bytes via rejection sampling
// in generateRecoveryCode to avoid modulo bias.
const recoveryCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// TOTPConfig configures a TOTPGenerator. All fields are optional; defaults
// follow RFC 6238 (SHA1 / 6 digits / 30-second period) and the otpauth://
// URI convention.
type TOTPConfig struct {
	// Issuer is the human-readable service name embedded in the otpauth://
	// URI. Defaults to "Velocity" when empty.
	Issuer string
	// Digits is the number of digits in generated codes. RFC 6238
	// recommends 6 or 8; defaults to 6.
	Digits int
	// Period is the time step in seconds. RFC 6238 recommends 30; defaults
	// to 30.
	Period int
	// Skew is the number of time steps tolerated on either side of the
	// current step when verifying. Defaults to 1 (i.e. previous, current,
	// and next windows are accepted). Use a negative value to disable
	// skew (strict current-window only).
	Skew int
}

// TOTPGenerator implements RFC 6238 TOTP with recovery codes. Methods are
// safe for concurrent use; the type holds only configuration data.
type TOTPGenerator struct {
	cfg TOTPConfig
}

// TOTP is the package-level generator with default configuration. Mirrors
// the documented surface (auth.TOTP.Generate, auth.TOTP.Verify, ...).
var TOTP = NewTOTP(TOTPConfig{Skew: 1})

// NewTOTP returns a TOTPGenerator with the supplied config; zero-valued
// fields (other than Skew, which defaults to 0 / strict matching) fall
// back to documented defaults. Pass Skew: 1 for the recommended
// previous/current/next acceptance window.
func NewTOTP(cfg TOTPConfig) *TOTPGenerator {
	if cfg.Issuer == "" {
		cfg.Issuer = "Velocity"
	}
	if cfg.Digits <= 0 {
		cfg.Digits = 6
	}
	if cfg.Period <= 0 {
		cfg.Period = 30
	}
	if cfg.Skew < 0 {
		cfg.Skew = 0
	}
	return &TOTPGenerator{cfg: cfg}
}

// Generate produces a new random base32-encoded secret and the matching
// otpauth:// URI for QR-code rendering. The label identifies the account
// (e.g. "user@example.com") and is URL-escaped per the otpauth spec.
func (g *TOTPGenerator) Generate(label string) (secret string, qrURL string, err error) {
	raw := make([]byte, totpSecretBytes)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	qrURL = g.buildOtpauthURL(label, secret)
	return secret, qrURL, nil
}

// Verify returns true when the supplied code matches the TOTP for secret
// within the configured skew window. All errors (decode failures, wrong
// length, mismatched code) collapse to false to avoid leaking why.
//
// Verify has NO replay protection: the same code submitted twice in the
// same window will succeed twice. For at-rest 2FA flows, callers SHOULD
// use VerifyAndConsume and persist the returned step per user.
func (g *TOTPGenerator) Verify(secret, code string) bool {
	if len(code) == 0 || len(secret) == 0 {
		return false
	}
	key, err := decodeSecret(secret)
	if err != nil {
		return false
	}
	step := time.Now().Unix() / int64(g.cfg.Period)
	supplied := []byte(code)

	// Compare every candidate (-skew..+skew) without short-circuiting so
	// timing does not reveal which window matched.
	var matched byte
	for offset := -g.cfg.Skew; offset <= g.cfg.Skew; offset++ {
		expected := generateCode(key, step+int64(offset), g.cfg.Digits)
		if len(expected) != len(supplied) {
			// Length mismatch always fails; constant-time compare requires
			// equal lengths so scheme explicitly.
			continue
		}
		matched |= byte(subtle.ConstantTimeCompare(expected, supplied))
	}
	return matched == 1
}

// VerifyAndConsume verifies code against secret and returns the matched
// step. The caller persists the returned step and rejects future calls
// where suppliedStep <= lastUsedStep to prevent replay within the skew
// window. Returns matched=false when the code is invalid.
//
// When lastUsedStep is non-zero and the matched step is <= lastUsedStep,
// VerifyAndConsume rejects the code at the framework level, returning
// (false, 0). This is the safer default; pass 0 on first verification.
//
// The implementation walks the entire skew window without short-circuiting
// so timing does not reveal which window (if any) matched. The matched
// step is selected via subtle.ConstantTimeSelect so the chosen step does
// not branch on data-dependent flow. On non-match (or replay rejection),
// the returned step is always 0; callers MUST treat (false, *) as "do
// nothing" and never use the step value.
func (g *TOTPGenerator) VerifyAndConsume(secret, code string, lastUsedStep int64) (matched bool, step int64) {
	if len(code) == 0 || len(secret) == 0 {
		return false, 0
	}
	key, err := decodeSecret(secret)
	if err != nil {
		return false, 0
	}
	currentStep := time.Now().Unix() / int64(g.cfg.Period)
	supplied := []byte(code)

	var matchedByte byte
	var matchedStep int64
	for offset := -g.cfg.Skew; offset <= g.cfg.Skew; offset++ {
		candidateStep := currentStep + int64(offset)
		expected := generateCode(key, candidateStep, g.cfg.Digits)
		var eq byte
		if len(expected) == len(supplied) {
			eq = byte(subtle.ConstantTimeCompare(expected, supplied))
		}
		// Record the first matching step without short-circuiting subsequent
		// comparisons. ConstantTimeSelect picks candidateStep into
		// matchedStep only when (eq == 1 && matchedByte == 0).
		notYet := 1 - int(matchedByte)
		updateNow := int(eq) & notYet
		// crypto/subtle.ConstantTimeSelect operates on int; for int64 we
		// build a mask manually below. matchedStep starts at 0 so an
		// inactive branch keeps it untouched.
		matchedStep = constantTimeSelectInt64(updateNow, candidateStep, matchedStep)
		matchedByte |= eq
	}

	if matchedByte != 1 {
		return false, 0
	}

	// Replay rejection: matched step must be strictly greater than the
	// caller's lastUsedStep. Fresh enrollments pass lastUsedStep == 0; any
	// real TOTP step (Unix/period) is far larger, so the comparison still
	// admits the first verify and rejects re-use of the same step. On
	// rejection we collapse to the public (false, 0) return shape so
	// callers cannot leak the matched step.
	if matchedStep <= lastUsedStep {
		return false, 0
	}
	return true, matchedStep
}

// constantTimeSelectInt64 returns x when v == 1 and y when v == 0. Mirrors
// crypto/subtle.ConstantTimeSelect for int64 values without leaking via a
// data-dependent branch.
func constantTimeSelectInt64(v int, x, y int64) int64 {
	// Build a mask of all-ones (v==1) or all-zeros (v==0) using arithmetic
	// only; no comparison or branching on v.
	mask := int64(-int64(v & 1))
	return (x & mask) | (y &^ mask)
}

// GenerateRecoveryCodes returns n single-use recovery codes formatted as
// XXXX-XXXX using the unambiguous alphabet. n must be positive.
func (g *TOTPGenerator) GenerateRecoveryCodes(n int) ([]string, error) {
	if n <= 0 {
		return nil, ErrInvalidRecoveryCount
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		code, err := generateRecoveryCode()
		if err != nil {
			return nil, err
		}
		out[i] = code
	}
	return out, nil
}

// ConsumeRecoveryCode performs a constant-time scan of stored against
// supplied. On match, returns true and a new slice with the matched code
// removed (single-use semantics). On no match, returns false and stored
// unchanged. The scan compares every entry (no early return) so timing
// cannot reveal which slot matched (or that no slot matched).
//
// Deprecated: this compares plaintext recovery codes. Storing recovery
// codes as plaintext at rest is unsafe. Use ConsumeRecoveryCodeHashed
// (with HashRecoveryCode at issuance time) for production.
func (g *TOTPGenerator) ConsumeRecoveryCode(stored []string, supplied string) (consumed bool, remaining []string) {
	if len(stored) == 0 || supplied == "" {
		return false, stored
	}
	suppliedBytes := []byte(supplied)

	matchIndex := -1
	var matched byte
	for i, code := range stored {
		var eq byte
		if len(code) == len(suppliedBytes) {
			eq = byte(subtle.ConstantTimeCompare([]byte(code), suppliedBytes))
		}
		// Record the first match without short-circuiting subsequent
		// comparisons. ConstantTimeSelect lets us update matchIndex only
		// when we have not yet matched.
		notYetMatched := 1 - int(matched)
		updateNow := int(eq) & notYetMatched
		matchIndex = subtle.ConstantTimeSelect(updateNow, i, matchIndex)
		matched |= eq
	}

	if matched != 1 {
		return false, stored
	}
	out := make([]string, 0, len(stored)-1)
	out = append(out, stored[:matchIndex]...)
	out = append(out, stored[matchIndex+1:]...)
	return true, out
}

// HashRecoveryCode returns a salted hash of code suitable for at-rest
// storage. It delegates to the supplied Hasher (typically the bcrypt
// hasher already configured for password hashing in the auth package);
// no new crypto dependency is introduced. Returns an error when h is
// nil or when code is empty.
func HashRecoveryCode(h Hasher, code string) (string, error) {
	if h == nil {
		return "", errors.New("auth: nil hasher")
	}
	if code == "" {
		return "", errors.New("auth: empty recovery code")
	}
	return h.Hash(code)
}

// ConsumeRecoveryCodeHashed is the hashed-storage analog of
// ConsumeRecoveryCode. The caller passes bcrypt (or otherwise Hasher-
// produced) hashes; we compare supplied against each hash with the
// configured Hasher and, on match, return the matched index and a new
// slice with that hash removed (single-use semantics).
//
// The scan walks the entire list with no early return so timing does not
// reveal which slot (or whether any slot) matched. Note that bcrypt
// itself is not strictly constant-time across hashes, but the per-hash
// cost dominates and the loop is uniform: every call performs len(stored)
// verifications, regardless of which (or whether any) hash matches.
//
// Returns:
//   - consumed=true, remaining without the matched hash, err=nil on match.
//   - consumed=false, original stored, err=nil on no match (stored is not
//     copied; do not mutate the returned slice).
//   - err only when h is nil or supplied is empty.
func (g *TOTPGenerator) ConsumeRecoveryCodeHashed(h Hasher, hashedStored []string, supplied string) (consumed bool, remaining []string, err error) {
	if h == nil {
		return false, hashedStored, errors.New("auth: nil hasher")
	}
	if supplied == "" {
		return false, hashedStored, errors.New("auth: empty supplied code")
	}
	if len(hashedStored) == 0 {
		return false, hashedStored, nil
	}

	matchIndex := -1
	var matched byte
	for i, hashed := range hashedStored {
		var eq byte
		// Hasher.Verify returns false for empty hashes; treat that as a
		// non-match without short-circuiting the rest of the scan.
		if h.Verify(supplied, hashed) {
			eq = 1
		}
		notYetMatched := 1 - int(matched)
		updateNow := int(eq) & notYetMatched
		matchIndex = subtle.ConstantTimeSelect(updateNow, i, matchIndex)
		matched |= eq
	}

	if matched != 1 {
		return false, hashedStored, nil
	}
	out := make([]string, 0, len(hashedStored)-1)
	out = append(out, hashedStored[:matchIndex]...)
	out = append(out, hashedStored[matchIndex+1:]...)
	return true, out, nil
}

// buildOtpauthURL constructs an otpauth://totp/<label>?... URI. Label is
// URL-escaped per RFC 6238 / Google Authenticator KeyURI conventions.
func (g *TOTPGenerator) buildOtpauthURL(label, secret string) string {
	u := url.URL{
		Scheme: "otpauth",
		Host:   "totp",
		// Path includes a leading "/"; PathEscape would double-encode "/"
		// inside the label, so we keep the label as-is and let net/url
		// escape it via URL.String().
		Path: "/" + label,
	}
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", g.cfg.Issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", itoa(g.cfg.Digits))
	q.Set("period", itoa(g.cfg.Period))
	u.RawQuery = q.Encode()
	return u.String()
}

// generateCode computes the HOTP value for key at step, formatted to the
// requested number of digits. RFC 4226 dynamic truncation; RFC 6238 uses
// it with step = floor(now / period).
func generateCode(key []byte, step int64, digits int) []byte {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))

	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0F
	binCode := (uint32(sum[offset])&0x7F)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])

	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	value := binCode % mod

	// Zero-pad to digits.
	out := make([]byte, digits)
	for i := digits - 1; i >= 0; i-- {
		out[i] = byte('0' + value%10)
		value /= 10
	}
	return out
}

// decodeSecret accepts both padded and unpadded base32 (case-insensitive)
// to be lenient with secrets pasted from external authenticator apps.
func decodeSecret(secret string) ([]byte, error) {
	upper := strings.ToUpper(strings.TrimSpace(secret))
	upper = strings.ReplaceAll(upper, " ", "")
	// Try unpadded first (our Generate format); fall back to padded.
	if b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(upper); err == nil {
		return b, nil
	}
	if b, err := base32.StdEncoding.DecodeString(upper); err == nil {
		return b, nil
	}
	return nil, ErrInvalidTOTPSecret
}

// generateRecoveryCode returns a single XXXX-XXXX recovery code drawn
// from the unambiguous alphabet using crypto/rand. Bytes that would
// introduce modulo bias against the 31-char alphabet are rejected.
func generateRecoveryCode() (string, error) {
	const half = 4
	const total = half * 2
	alphaLen := byte(len(recoveryCodeAlphabet))
	// Largest multiple of alphaLen that fits in a byte; bytes >= limit
	// are rejected to keep the distribution uniform.
	limit := byte(256 - (256 % int(alphaLen)))

	picked := make([]byte, 0, total)
	buf := make([]byte, total)
	for len(picked) < total {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b >= limit {
				continue
			}
			picked = append(picked, recoveryCodeAlphabet[b%alphaLen])
			if len(picked) == total {
				break
			}
		}
	}
	out := make([]byte, total+1)
	copy(out[:half], picked[:half])
	out[half] = '-'
	copy(out[half+1:], picked[half:])
	return string(out), nil
}

// itoa is a small allocation-light integer formatter for the otpauth URI.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
