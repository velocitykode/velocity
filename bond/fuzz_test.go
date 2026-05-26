package bond

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/router"
)

// FuzzReadFlashCookie feeds arbitrary strings as the flash cookie value.
// Flash cookies are now authenticated under the app's crypto.Encryptor,
// but the read path must still tolerate any byte sequence a hostile
// browser might present without panicking.
//
// Contract:
//  1. Never panic.
//  2. On any error (oversized, malformed envelope, AAD mismatch, wrong
//     key, invalid JSON inside an otherwise-valid ciphertext), return
//     (nil, false). A partial decode that a template might render as
//     the attacker's injected shape is the bug we are guarding against.
//
// Run ad-hoc: go test -run=^$ -fuzz=FuzzReadFlashCookie -fuzztime=30s ./bond
func FuzzReadFlashCookie(f *testing.F) {
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    "base64:MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		f.Fatalf("failed to build fuzz encryptor: %v", err)
	}

	seeds := []string{
		"",
		"not-base64!!!",
		"eyJmb28iOiJiYXIifQ==", // valid base64 of {"foo":"bar"} but wrong charset for URLEncoding
		"eyJmb28iOiJiYXIifQ",   // URL-base64 of same
		"////",
		"\x00\x01\x02",
		"AAAA",
		"e30",    // base64("{}")
		"bnVsbA", // base64("null")
		"v1:",    // envelope prefix only
	}
	// Also seed with a real ciphertext so the fuzzer has a known-good
	// payload to mutate. Mutation strategies should reach corruption,
	// truncation, and AAD-mismatch paths quickly.
	if sealed, sealErr := router.SealFlash(enc, router.FlashErrorsCookie, map[string]any{"k": "v"}); sealErr == nil {
		seeds = append(seeds, sealed)
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, value string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on value %q: %v", value, r)
			}
		}()

		r := httptest.NewRequest("GET", "/", nil)
		r = router.WithServices(r, &app.Services{Crypto: enc})
		// AddCookie silently drops CR/LF; encode those safely.
		r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: sanitizeCookieValue(value)})

		result, ok := readFlashCookie(r, flashErrorsCookie)
		if !ok && result != nil {
			t.Errorf("readFlashCookie returned (non-nil, false); caller would leak the partial decode")
		}
	})
}

// sanitizeCookieValue strips bytes that net/http's AddCookie refuses to
// set. A cookie with CR/LF gets dropped entirely, which would skip the
// code path we're trying to fuzz. We rely on readFlashCookie handling
// the value that actually made it into the header.
func sanitizeCookieValue(v string) string {
	out := make([]byte, 0, len(v))
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == 0x00 || c == '\r' || c == '\n' || c == ' ' || c == '"' || c == ',' || c == ';' || c == '\\' {
			continue
		}
		if c < 0x20 || c > 0x7e {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}
