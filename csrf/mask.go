package csrf

import (
	"crypto/rand"
	"encoding/base64"
)

// encodedTokenLength is the byte length of the string form of a
// framework-issued token: base64.URLEncoding over tokenLength random
// bytes (see GenerateToken).
var encodedTokenLength = base64.URLEncoding.EncodedLen(tokenLength)

// MaskToken wraps a stored CSRF token in a fresh per-response mask so the
// same underlying token never appears as identical bytes in two responses.
// A token repeated verbatim across responses (XSRF-TOKEN cookie, <meta>
// tag, page props) is extractable byte-by-byte by a compression-oracle
// attack (BREACH) when TLS-level compression and an attacker-controlled
// reflection coexist on the page. Masking removes the cross-response
// repetition the oracle needs.
//
// The masked form is base64(nonce || nonce XOR token) where nonce is
// len(token) bytes from crypto/rand and the XOR runs over the token's
// string bytes. This is an ENCODING, not encryption: it adds no secret
// material, the stored token is untouched, and anyone holding the masked
// value can trivially recover the token. Its only job is to make every
// emission unique.
//
// Every emission of a token to a response MUST go through MaskToken (the
// XSRF-TOKEN cookie writes, TokenForRequest, and RefreshHandler already
// do). Validation accepts both the masked form and the raw token, so
// values captured before masking shipped keep working during rollout.
//
// A token whose length is not the framework-issued length (a custom
// store seeded with foreign values) is returned unchanged: the unmask
// step detects the masked form purely by length, so a nonstandard-length
// token cannot round-trip and is emitted raw. Note that the middleware
// only accepts framework-length raw values and well-formed masked
// values (see decodeRequestToken); a nonstandard-length value submitted
// on a request is treated as a missing token. The only error path is
// crypto/rand failure.
func MaskToken(token string) (string, error) {
	if len(token) != encodedTokenLength {
		return token, nil
	}
	buf := make([]byte, 2*encodedTokenLength)
	nonce := buf[:encodedTokenLength]
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	for i := 0; i < encodedTokenLength; i++ {
		buf[encodedTokenLength+i] = nonce[i] ^ token[i]
	}
	return base64.URLEncoding.EncodeToString(buf), nil
}

// tokenEncoding classifies the wire form of a request-supplied token
// value, as detected by decodeRequestToken.
type tokenEncoding int

const (
	// encodingRaw is a value of exactly the framework-issued token
	// length, accepted as-is during the masking transition (legacy
	// clients echoing a token captured before masking shipped, or a
	// raw GetToken read).
	encodingRaw tokenEncoding = iota
	// encodingMasked is a well-formed masked value (see MaskToken)
	// whose underlying token was recovered.
	encodingMasked
	// encodingMalformed is everything else: not framework token length
	// and not a well-formed masked value (bad base64, or a decoded
	// length other than exactly 2x the token length - truncated or
	// padded masks). The middleware treats these as no token submitted.
	encodingMalformed
)

// decodeRequestToken classifies value and, when it is the masked form,
// recovers the underlying token. A value is treated as masked if and
// only if it base64-decodes to exactly 2x the framework token length;
// the recovered token is nonce XOR second-half. Decoding never
// validates anything: for the raw and masked encodings the terminal
// constant-time comparison against the stored token remains the single
// accept/reject decision point. Malformed values are returned unchanged
// alongside encodingMalformed; the caller decides their fate (the
// middleware maps them to ErrTokenMissing before any store access).
func decodeRequestToken(value string) (string, tokenEncoding) {
	if len(value) == encodedTokenLength {
		return value, encodingRaw
	}
	decoded, err := base64.URLEncoding.DecodeString(value)
	if err != nil || len(decoded) != 2*encodedTokenLength {
		return value, encodingMalformed
	}
	token := make([]byte, encodedTokenLength)
	for i := 0; i < encodedTokenLength; i++ {
		token[i] = decoded[i] ^ decoded[encodedTokenLength+i]
	}
	return string(token), encodingMasked
}

// UnmaskToken reverses MaskToken: a well-formed masked value (see
// decodeRequestToken) yields the underlying token; any other value
// (raw legacy token, garbage, truncated mask) is returned unchanged.
// UnmaskToken itself never validates anything.
func UnmaskToken(value string) string {
	token, encoding := decodeRequestToken(value)
	if encoding == encodingMasked {
		return token
	}
	return value
}
