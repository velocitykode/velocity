package csrf

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/velocitykode/velocity/csrf/stores"
)

// maskTestCSRF returns a CSRF instance with a seeded token so tests can
// compare emissions against a known stored value.
func maskTestCSRF(t *testing.T, sessionID string) (*CSRF, string) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.SessionIDResolver = testCookieResolver("session_id")
	cfg.Store = stores.NewSessionStore()
	c := New(cfg)
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if err := c.config.Store.Set(sessionID, token); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	return c, token
}

// xsrfCookieValue runs one GET through the middleware and returns the
// URL-decoded XSRF-TOKEN value.
func xsrfCookieValue(t *testing.T, c *CSRF, sessionID string) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})
	w := httptest.NewRecorder()
	c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(w, req)
	for _, ck := range w.Result().Cookies() {
		if ck.Name == "XSRF-TOKEN" {
			decoded, err := url.QueryUnescape(ck.Value)
			if err != nil {
				t.Fatalf("XSRF-TOKEN not URL-encoded: %v", err)
			}
			return decoded
		}
	}
	t.Fatal("XSRF-TOKEN cookie not written on safe-method GET")
	return ""
}

// postWithToken submits a POST carrying value in the configured header
// and returns the response status code.
func postWithToken(c *CSRF, sessionID, value string) int {
	req := httptest.NewRequest("POST", "/", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})
	req.Header.Set(c.config.HeaderName, value)
	w := httptest.NewRecorder()
	c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(w, req)
	return w.Code
}

// TestMaskToken_RoundTrip pins the encoding contract: masked form
// base64-decodes to exactly twice the token length and unmasks back to
// the original token.
func TestMaskToken_RoundTrip(t *testing.T) {
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	masked, err := MaskToken(token)
	if err != nil {
		t.Fatalf("MaskToken: %v", err)
	}
	if masked == token {
		t.Fatal("MaskToken returned the input unchanged for a framework-length token")
	}
	decoded, err := base64.URLEncoding.DecodeString(masked)
	if err != nil {
		t.Fatalf("masked form is not valid base64: %v", err)
	}
	if len(decoded) != 2*encodedTokenLength {
		t.Fatalf("masked form decodes to %d bytes, want %d", len(decoded), 2*encodedTokenLength)
	}
	if got := UnmaskToken(masked); got != token {
		t.Errorf("UnmaskToken(MaskToken(token)) = %q, want %q", got, token)
	}
}

// TestMaskToken_FreshPerCall pins the BREACH property at the unit level:
// two masks of the same token are different byte strings.
func TestMaskToken_FreshPerCall(t *testing.T) {
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	m1, err := MaskToken(token)
	if err != nil {
		t.Fatalf("MaskToken #1: %v", err)
	}
	m2, err := MaskToken(token)
	if err != nil {
		t.Fatalf("MaskToken #2: %v", err)
	}
	if m1 == m2 {
		t.Error("two MaskToken calls produced identical output; nonce is not fresh per emission")
	}
}

// TestMaskToken_NonstandardLengthPassthrough pins the custom-store
// escape hatch: a token that is not the framework-issued length is
// emitted unchanged (it could not be detected for unmasking), and
// UnmaskToken leaves it alone too.
func TestMaskToken_NonstandardLengthPassthrough(t *testing.T) {
	const odd = "operator-seeded-token"
	masked, err := MaskToken(odd)
	if err != nil {
		t.Fatalf("MaskToken: %v", err)
	}
	if masked != odd {
		t.Errorf("MaskToken(%q) = %q, want passthrough", odd, masked)
	}
	if got := UnmaskToken(odd); got != odd {
		t.Errorf("UnmaskToken(%q) = %q, want passthrough", odd, got)
	}
}

// TestUnmaskToken_StrictLength pins that only a value decoding to
// EXACTLY 2x the token length is treated as masked; everything else is
// passed through untouched for the terminal constant-time comparison
// to reject.
func TestUnmaskToken_StrictLength(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"not base64", "%%%not-base64%%%"},
		{"raw token length", base64.URLEncoding.EncodeToString(make([]byte, tokenLength))},
		{"one byte short of masked", base64.URLEncoding.EncodeToString(make([]byte, 2*encodedTokenLength-1))},
		{"one byte past masked", base64.URLEncoding.EncodeToString(make([]byte, 2*encodedTokenLength+1))},
		{"empty", ""},
	} {
		if got := UnmaskToken(tc.in); got != tc.in {
			t.Errorf("%s: UnmaskToken(%q) = %q, want input unchanged", tc.name, tc.in, got)
		}
	}
}

// TestMasking_ConsecutiveResponsesDifferAndBothValidate is the
// end-to-end BREACH pin: two GETs for the same session emit different
// XSRF-TOKEN bytes, neither equals the raw stored token, and BOTH
// validate when echoed on a POST. The accept decision for each is still
// the constant-time comparison in validateToken (ValidateToken /
// crypto/subtle); unmasking only decodes.
func TestMasking_ConsecutiveResponsesDifferAndBothValidate(t *testing.T) {
	const sessionID = "mask-sess"
	c, stored := maskTestCSRF(t, sessionID)

	first := xsrfCookieValue(t, c, sessionID)
	second := xsrfCookieValue(t, c, sessionID)

	if first == second {
		t.Errorf("consecutive responses emitted identical token bytes %q; mask must be fresh per response", first)
	}
	if first == stored || second == stored {
		t.Error("a response emitted the raw stored token; every emission must be masked")
	}
	if code := postWithToken(c, sessionID, first); code != http.StatusOK {
		t.Errorf("first response's masked token rejected: status %d", code)
	}
	if code := postWithToken(c, sessionID, second); code != http.StatusOK {
		t.Errorf("second response's masked token rejected: status %d", code)
	}
}

// TestMasking_TamperedMaskedValueRejected pins that flipping one byte
// of a masked value unmasks to a different candidate and fails the
// constant-time comparison with 419.
func TestMasking_TamperedMaskedValueRejected(t *testing.T) {
	const sessionID = "tamper-sess"
	c, _ := maskTestCSRF(t, sessionID)

	masked := xsrfCookieValue(t, c, sessionID)
	decoded, err := base64.URLEncoding.DecodeString(masked)
	if err != nil {
		t.Fatalf("decode masked: %v", err)
	}
	// Flip one bit in the XOR-ed half so the recovered token changes
	// while the length (and thus masked-form detection) is preserved.
	decoded[len(decoded)-1] ^= 0x01
	tampered := base64.URLEncoding.EncodeToString(decoded)

	if code := postWithToken(c, sessionID, tampered); code != 419 {
		t.Errorf("tampered masked token accepted: status %d, want 419", code)
	}
}

// TestMasking_RawLegacyTokenStillValidates pins the transition
// contract: a client holding the raw stored token (captured before
// masking shipped, or read via GetToken) can still submit it directly.
func TestMasking_RawLegacyTokenStillValidates(t *testing.T) {
	const sessionID = "legacy-sess"
	c, stored := maskTestCSRF(t, sessionID)

	if code := postWithToken(c, sessionID, stored); code != http.StatusOK {
		t.Errorf("raw legacy token rejected: status %d, want 200", code)
	}
}

// TestMasking_MalformedValueTreatedAsMissing pins the error
// classification a custom ErrorHandler observes: a value that is
// neither a raw framework-length token nor a well-formed masked value
// (bad base64, truncated mask, wrong decoded length) is rejected as
// ErrTokenMissing before any store lookup, exactly like an absent
// token. A framework-length raw value that simply does not match stays
// ErrTokenInvalid: it reaches the constant-time compare.
func TestMasking_MalformedValueTreatedAsMissing(t *testing.T) {
	const sessionID = "malformed-sess"
	c, stored := maskTestCSRF(t, sessionID)

	var observed error
	c.config.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		observed = err
		w.WriteHeader(419)
	}

	masked, err := MaskToken(stored)
	if err != nil {
		t.Fatalf("MaskToken: %v", err)
	}
	wrongRaw, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	for _, tc := range []struct {
		name  string
		value string
		want  error
	}{
		{"not base64", "%%%not-base64%%%", ErrTokenMissing},
		{"truncated mask", masked[:len(masked)-4], ErrTokenMissing},
		{"decodes short of masked", base64.URLEncoding.EncodeToString(make([]byte, 2*encodedTokenLength-1)), ErrTokenMissing},
		{"decodes past masked", base64.URLEncoding.EncodeToString(make([]byte, 2*encodedTokenLength+1)), ErrTokenMissing},
		{"wrong raw framework-length token", wrongRaw, ErrTokenInvalid},
	} {
		observed = nil
		if code := postWithToken(c, sessionID, tc.value); code != 419 {
			t.Errorf("%s: status %d, want 419", tc.name, code)
		}
		if !errors.Is(observed, tc.want) {
			t.Errorf("%s: ErrorHandler observed %v, want %v", tc.name, observed, tc.want)
		}
	}

	// The seeded token must still validate after the rejected attempts.
	if code := postWithToken(c, sessionID, masked); code != http.StatusOK {
		t.Errorf("masked token rejected after malformed attempts: status %d", code)
	}
}

// TestMasking_RefreshHandlerEmitsMaskedToken pins the JSON sink: the
// refresh endpoint must not return the raw stored token, and the masked
// value it returns must validate.
func TestMasking_RefreshHandlerEmitsMaskedToken(t *testing.T) {
	const sessionID = "refresh-sess"
	c, _ := maskTestCSRF(t, sessionID)

	req := httptest.NewRequest("POST", "/csrf/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})
	w := httptest.NewRecorder()
	c.RefreshHandler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("RefreshHandler status %d", w.Code)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode refresh body: %v", err)
	}
	stored, err := c.config.Store.Get(sessionID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if body.Token == stored {
		t.Error("RefreshHandler emitted the raw stored token; must be masked")
	}
	if got := UnmaskToken(body.Token); got != stored {
		t.Errorf("UnmaskToken(refresh token) = %q, want stored %q", got, stored)
	}
	if code := postWithToken(c, sessionID, body.Token); code != http.StatusOK {
		t.Errorf("masked refresh token rejected: status %d", code)
	}
}
