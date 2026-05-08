package auth

import (
	"strings"
	"testing"
)

// FuzzJWTValidate feeds arbitrary strings into JWTManager.ValidateToken.
// The contract under fuzzing is:
//  1. Never panic, regardless of input shape.
//  2. Never return (claims, nil) for obviously malformed or tampered
//     tokens (seeds cover: empty, single segment, missing payload,
//     "alg=none" smuggled, truncated signature, unicode junk).
//  3. Never leak the HMAC secret into the error message — a regression
//     that put %v of the key into the error would surface here.
//
// Run ad-hoc: go test -run=^$ -fuzz=FuzzJWTValidate -fuzztime=30s ./auth
func FuzzJWTValidate(f *testing.F) {
	mgr, err := NewJWTManager(JWTConfig{
		Secret:    strings.Repeat("k", 32),
		Algorithm: "HS256",
		TTL:       5,
	})
	if err != nil {
		f.Fatalf("NewJWTManager: %v", err)
	}

	seeds := []string{
		"",
		".",
		"..",
		"a.b.c",
		"eyJhbGciOiJub25lIn0..", // alg=none header
		"eyJhbGciOiJIUzI1NiJ9.eyJ1aWQiOjF9.",
		"eyJhbGciOiJIUzI1NiJ9.eyJ1aWQiOjF9.AAAA",
		"\x00\x00\x00",
		strings.Repeat("a.b.c", 1000),
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.e30.sig",
		// Base64 padding / URL-base64 mixups that have historically tripped decoders.
		"eyJhbGciOiJIUzI1NiJ9===.e30.sig",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, token string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic validating %q: %v", token, r)
			}
		}()

		claims, err := mgr.ValidateToken(token)

		// Contract: random input must not authenticate. The fuzzer is
		// astronomically unlikely to generate a validly-signed token
		// with the secret it doesn't know.
		if err == nil && claims != nil {
			t.Errorf("validated a random token as %+v", claims)
		}

		// Contract: the error must not include the HMAC secret. We seeded
		// the manager with "kkkkkkkk..." so if that appears in an error
		// message, the key is leaking.
		if err != nil && strings.Contains(err.Error(), strings.Repeat("k", 32)) {
			t.Errorf("error message leaks HMAC secret: %v", err)
		}
	})
}
