package bond

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// FuzzReadFlashCookie feeds arbitrary strings as the flash cookie value.
// Flash cookies round-trip unauthenticated user input (validation errors
// from a prior request), so a panic on a malformed value would be a
// crash-via-cookie DoS.
//
// Contract:
//  1. Never panic.
//  2. On any error (invalid base64 or invalid JSON), return (nil, false)
//     — never partial data that a template would happily render as the
//     attacker's injected shape.
//
// Run ad-hoc: go test -run=^$ -fuzz=FuzzReadFlashCookie -fuzztime=30s ./bond
func FuzzReadFlashCookie(f *testing.F) {
	seeds := []string{
		"",
		"not-base64!!!",
		"eyJmb28iOiJiYXIifQ==", // valid base64 of {"foo":"bar"} but wrong charset for URLEncoding
		"eyJmb28iOiJiYXIifQ",   // URL-base64 of same
		"////",
		"\x00\x01\x02",
		"AAAA",
		"e30",  // base64("{}")
		"bnVsbA", // base64("null")
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
		// AddCookie silently drops CR/LF; encode those safely.
		r.AddCookie(&http.Cookie{Name: flashErrorsCookie, Value: sanitizeCookieValue(value)})

		result, ok := readFlashCookie(r, flashErrorsCookie)
		if !ok && result != nil {
			t.Errorf("readFlashCookie returned (non-nil, false); caller would leak the partial decode")
		}
	})
}

// sanitizeCookieValue strips bytes that net/http's AddCookie refuses to
// set — a cookie with CR/LF gets dropped entirely, which would skip the
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
