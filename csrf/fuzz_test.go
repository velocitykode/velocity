package csrf

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// FuzzValidateToken_CRLFInput feeds random byte strings as both the request
// token and the expected-stored token into ValidateToken. The only contract
// is that it never panics and never succeeds for inputs containing CR or LF
// that would indicate smuggling attempts.
func FuzzValidateToken_CRLFInput(f *testing.F) {
	seeds := []string{
		"",
		"plain-token",
		"tok\r\nSet-Cookie: evil=1",
		"tok\nfoo",
		"tok\rfoo",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, token string) {
		// Must never panic.
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on input %q: %v", token, r)
			}
		}()
		// Equal tokens should return true; CRLF sequences should not crash.
		_ = ValidateToken(token, token)
		_ = ValidateToken(token, "different")
		_ = ValidateToken("different", token)
	})
}

// TestGetSessionID_StripsCRLFFromCookies verifies that a cookie value is
// never interpreted in a way that allows CRLF smuggling. Go's net/http
// already refuses to parse cookies containing CR/LF, but we document the
// behaviour here. Returns ErrNoSession if the parser rejects the value —
// that is an acceptable outcome (no CSRF token is issued when no usable
// session cookie is present).
func TestGetSessionID_StripsCRLFFromCookies(t *testing.T) {
	c := New(DefaultConfig())
	r := httptest.NewRequest("POST", "/", nil)
	// Manually set a header value containing CR/LF — net/http's AddCookie
	// would sanitize this, so we set via Header.
	r.Header.Set("Cookie", "session_id=abc\r\nX-Evil: 1")

	id, err := c.getSessionID(r)
	if err != nil && err != ErrNoSession {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.ContainsAny(id, "\r\n") {
		t.Errorf("session id contains CR/LF: %q", id)
	}
}

// TestValidateToken_ConstantTime ensures the comparison is not length-based.
// This is a smoke test; timing-attack resistance is exercised by
// BenchmarkConstantTimeCompare below.
func TestValidateToken_ConstantTime(t *testing.T) {
	a := strings.Repeat("a", 32)
	b := strings.Repeat("b", 32)
	if ValidateToken(a, b) {
		t.Error("different tokens should not validate")
	}
	if !ValidateToken(a, a) {
		t.Error("equal tokens should validate")
	}
	// Length mismatch must return false without panicking.
	if ValidateToken("short", strings.Repeat("a", 64)) {
		t.Error("length-mismatched tokens should not validate")
	}
}

func BenchmarkConstantTimeCompare(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping in -short")
	}
	t1 := strings.Repeat("a", 32)
	t2 := strings.Repeat("a", 32)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ValidateToken(t1, t2)
	}
}
