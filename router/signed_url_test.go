package router

import (
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testRouterWithSignedKey returns a router pre-populated with a random
// 32-byte signed-URL HMAC key and a single named route. The route is
// committed (frozen) so URL generation works immediately.
func testRouterWithSignedKey(t *testing.T) *VelocityRouterV2 {
	t.Helper()
	r := NewV2()
	r.Get("/orders/{id}", dummyHandler).Name("orders.show")

	// Commit by serving one request.
	req := httptest.NewRequest("GET", "/orders/1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	r.SetSignedURLKey(key)
	return r
}

func TestSignedURL_RoundTrip(t *testing.T) {
	r := testRouterWithSignedKey(t)

	signed, err := r.TemporarySignedURL("orders.show", map[string]string{"id": "42"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	if !strings.Contains(signed, "signature=") || !strings.Contains(signed, "expires=") {
		t.Fatalf("expected signature and expires in URL, got %q", signed)
	}

	req := httptest.NewRequest("GET", signed, nil)
	if err := r.ValidateSignature(req); err != nil {
		t.Fatalf("ValidateSignature on round-trip URL: %v", err)
	}
	if !r.HasValidSignature(req) {
		t.Fatal("HasValidSignature returned false for valid URL")
	}
}

func TestSignedURL_NoExpiry(t *testing.T) {
	r := testRouterWithSignedKey(t)

	// Pass zero time: URL is still signed, no expiry parameter.
	signed, err := r.SignedURL("orders.show", map[string]string{"id": "9"}, nil, time.Time{})
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	if strings.Contains(signed, "expires=") {
		t.Fatalf("expected no expires in URL, got %q", signed)
	}
	req := httptest.NewRequest("GET", signed, nil)
	if err := r.ValidateSignature(req); err != nil {
		t.Fatalf("ValidateSignature for no-expiry URL: %v", err)
	}
}

func TestSignedURL_ExpiredRejected(t *testing.T) {
	r := testRouterWithSignedKey(t)

	// Use SignedURL with an explicitly past time so the URL is already
	// expired when we verify it.
	signed, err := r.SignedURL("orders.show", map[string]string{"id": "1"}, nil, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	req := httptest.NewRequest("GET", signed, nil)
	err = r.ValidateSignature(req)
	if !errors.Is(err, ErrSignatureExpired) {
		t.Fatalf("expected ErrSignatureExpired, got %v", err)
	}
}

func TestSignedURL_TamperedPathRejected(t *testing.T) {
	r := testRouterWithSignedKey(t)

	signed, err := r.TemporarySignedURL("orders.show", map[string]string{"id": "1"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}

	// Swap the path: the signature was minted for /orders/1, so re-sending
	// the same query against /orders/2 must fail.
	u, _ := url.Parse(signed)
	u.Path = "/orders/2"
	req := httptest.NewRequest("GET", u.String(), nil)
	err = r.ValidateSignature(req)
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid on path tamper, got %v", err)
	}
}

func TestSignedURL_TamperedExpiresRejected(t *testing.T) {
	r := testRouterWithSignedKey(t)

	signed, err := r.TemporarySignedURL("orders.show", map[string]string{"id": "1"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}

	// Push expires far into the future without re-signing: signature
	// commits to the original expires, so this must be invalid (NOT
	// expired, because the new expires is in the future).
	u, _ := url.Parse(signed)
	q := u.Query()
	q.Set("expires", strconv.FormatInt(time.Now().Add(72*time.Hour).Unix(), 10))
	u.RawQuery = q.Encode()
	req := httptest.NewRequest("GET", u.String(), nil)
	err = r.ValidateSignature(req)
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid on expires tamper, got %v", err)
	}
}

func TestSignedURL_QueryReorderStillValidates(t *testing.T) {
	r := testRouterWithSignedKey(t)

	extra := url.Values{}
	extra.Set("z_last", "1")
	extra.Set("a_first", "2")
	extra.Set("m_middle", "3")

	signed, err := r.TemporarySignedURL("orders.show", map[string]string{"id": "5"}, extra, time.Hour)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}

	// Manually re-emit the query in arbitrary (non-sorted) order. Both
	// sides canonicalise to sorted form, so the signature still verifies.
	u, _ := url.Parse(signed)
	q := u.Query()
	parts := []string{
		"signature=" + url.QueryEscape(q.Get("signature")),
		"expires=" + url.QueryEscape(q.Get("expires")),
		"z_last=" + url.QueryEscape(q.Get("z_last")),
		"a_first=" + url.QueryEscape(q.Get("a_first")),
		"m_middle=" + url.QueryEscape(q.Get("m_middle")),
	}
	u.RawQuery = strings.Join(parts, "&")
	req := httptest.NewRequest("GET", u.String(), nil)

	if err := r.ValidateSignature(req); err != nil {
		t.Fatalf("expected reordered query to still validate, got %v", err)
	}
}

func TestSignedURL_TamperedExtraQueryRejected(t *testing.T) {
	r := testRouterWithSignedKey(t)

	extra := url.Values{}
	extra.Set("download", "pdf")
	signed, err := r.TemporarySignedURL("orders.show", map[string]string{"id": "1"}, extra, time.Hour)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	u, _ := url.Parse(signed)
	q := u.Query()
	q.Set("download", "exe")
	u.RawQuery = q.Encode()
	req := httptest.NewRequest("GET", u.String(), nil)
	if err := r.ValidateSignature(req); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid on extra-query tamper, got %v", err)
	}
}

func TestSignedURL_MissingSignature(t *testing.T) {
	r := testRouterWithSignedKey(t)

	req := httptest.NewRequest("GET", "/orders/1", nil)
	if err := r.ValidateSignature(req); !errors.Is(err, ErrSignatureMissing) {
		t.Fatalf("expected ErrSignatureMissing, got %v", err)
	}
}

func TestSignedURL_MalformedExpires(t *testing.T) {
	r := testRouterWithSignedKey(t)

	// Hand-craft a URL with a non-numeric expires; the verifier must
	// reject as invalid (NOT expired), so the operator sees the right
	// failure mode.
	req := httptest.NewRequest("GET", "/orders/1?expires=notanumber&signature=deadbeef", nil)
	err := r.ValidateSignature(req)
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid on malformed expires, got %v", err)
	}
}

func TestSignedURL_KeyMissingErrorClass(t *testing.T) {
	r := NewV2()
	r.Get("/orders/{id}", dummyHandler).Name("orders.show")
	req0 := httptest.NewRequest("GET", "/orders/1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req0)
	// SetSignedURLKey never called.

	if _, err := r.TemporarySignedURL("orders.show", map[string]string{"id": "1"}, nil, time.Hour); !errors.Is(err, ErrSignedURLKeyMissing) {
		t.Fatalf("expected ErrSignedURLKeyMissing on mint, got %v", err)
	}
	req := httptest.NewRequest("GET", "/orders/1?expires=1&signature=abc", nil)
	if err := r.ValidateSignature(req); !errors.Is(err, ErrSignedURLKeyMissing) {
		t.Fatalf("expected ErrSignedURLKeyMissing on verify, got %v", err)
	}
}

func TestSignedURL_ReservedQueryParamsRejected(t *testing.T) {
	r := testRouterWithSignedKey(t)

	for _, name := range []string{"signature", "expires"} {
		extra := url.Values{}
		extra.Set(name, "smuggled")
		if _, err := r.TemporarySignedURL("orders.show", map[string]string{"id": "1"}, extra, time.Hour); err == nil {
			t.Fatalf("expected error when extraQuery contains reserved %q", name)
		}
	}
}

func TestSignedURL_DifferentKeyRejected(t *testing.T) {
	r1 := testRouterWithSignedKey(t)
	r2 := testRouterWithSignedKey(t) // independent random key

	signed, err := r1.TemporarySignedURL("orders.show", map[string]string{"id": "1"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	req := httptest.NewRequest("GET", signed, nil)
	if err := r2.ValidateSignature(req); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid when verifying with foreign key, got %v", err)
	}
}

func TestSignedURL_SignedMiddlewareReturnsForbidden(t *testing.T) {
	r := testRouterWithSignedKey(t)

	mw := r.SignedMiddleware()
	called := false
	wrapped := mw(func(c *Context) error {
		called = true
		return nil
	})

	// Tampered URL: signature missing.
	req := httptest.NewRequest("GET", "/orders/1", nil)
	c, _ := NewTestContext("GET", "/orders/1")
	c.Request = req
	err := wrapped(c)
	if called {
		t.Fatal("handler should not be called on signature failure")
	}
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("expected *HTTPError, got %T (%v)", err, err)
	}
	if httpErr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", httpErr.Code)
	}
	if !errors.Is(httpErr.Internal, ErrSignatureMissing) {
		t.Errorf("expected Internal to wrap ErrSignatureMissing, got %v", httpErr.Internal)
	}
}

func TestSignedURL_SignedMiddlewareAllowsValid(t *testing.T) {
	r := testRouterWithSignedKey(t)

	signed, err := r.TemporarySignedURL("orders.show", map[string]string{"id": "1"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}

	mw := r.SignedMiddleware()
	called := false
	wrapped := mw(func(c *Context) error {
		called = true
		return nil
	})

	req := httptest.NewRequest("GET", signed, nil)
	c, _ := NewTestContext("GET", signed)
	c.Request = req
	if err := wrapped(c); err != nil {
		t.Fatalf("middleware rejected valid signature: %v", err)
	}
	if !called {
		t.Fatal("handler not called for valid signature")
	}
}

// TestSignedURL_SignedMiddlewareFailsClosedNoKey_Unsigned is the M-16
// regression for the unsigned-request branch: a router with no
// signed-URL key MUST reject any request through SignedMiddleware with
// 403, even when the request carries no signature param. The previous
// fail-open behaviour silently downgraded a protected signed route to
// an unsigned route whenever APP_KEY was empty.
func TestSignedURL_SignedMiddlewareFailsClosedNoKey_Unsigned(t *testing.T) {
	r := NewV2()
	r.Get("/orders/{id}", dummyHandler).Name("orders.show")
	req0 := httptest.NewRequest("GET", "/orders/1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req0)
	// No SetSignedURLKey: fail-closed 403 with ErrSignedURLKeyMissing.

	mw := r.SignedMiddleware()
	called := false
	wrapped := mw(func(c *Context) error {
		called = true
		return nil
	})

	req := httptest.NewRequest("GET", "/orders/1", nil)
	c, _ := NewTestContext("GET", "/orders/1")
	c.Request = req
	err := wrapped(c)
	if called {
		t.Fatal("handler must not be called when signed-URL key is missing")
	}
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("expected *HTTPError, got %T (%v)", err, err)
	}
	if httpErr.Code != http.StatusForbidden {
		t.Errorf("expected 403 when key missing, got %d", httpErr.Code)
	}
	if !errors.Is(httpErr.Internal, ErrSignedURLKeyMissing) {
		t.Errorf("expected Internal to wrap ErrSignedURLKeyMissing, got %v", httpErr.Internal)
	}
}

// TestSignedURL_SignedMiddlewareFailsClosedNoKey_ValidSig is the M-16
// regression for the worst-case branch: an attacker crafts a request
// that LOOKS like a valid signed URL (e.g. captured from another
// environment or replayed) and the production deployment has lost its
// APP_KEY. Without the fail-closed fix, the middleware would pass the
// request through unchanged. With the fix, the absence of a key means
// the router cannot prove the signature is valid, so it MUST reject.
func TestSignedURL_SignedMiddlewareFailsClosedNoKey_ValidSig(t *testing.T) {
	// Mint a URL with a real key, then point the verifier at a
	// freshly-constructed router that has no key. The middleware must
	// 403 without consulting the URL contents (no oracle leakage).
	rMinter := testRouterWithSignedKey(t)
	signed, err := rMinter.TemporarySignedURL("orders.show", map[string]string{"id": "1"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}

	rVerifier := NewV2()
	rVerifier.Get("/orders/{id}", dummyHandler).Name("orders.show")
	req0 := httptest.NewRequest("GET", "/orders/1", nil)
	w := httptest.NewRecorder()
	rVerifier.ServeHTTP(w, req0)
	// No SetSignedURLKey on rVerifier.

	mw := rVerifier.SignedMiddleware()
	called := false
	wrapped := mw(func(c *Context) error {
		called = true
		return nil
	})

	req := httptest.NewRequest("GET", signed, nil)
	c, _ := NewTestContext("GET", signed)
	c.Request = req
	err = wrapped(c)
	if called {
		t.Fatal("handler must not be called when signed-URL key is missing, even with a syntactically valid signature")
	}
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("expected *HTTPError, got %T (%v)", err, err)
	}
	if httpErr.Code != http.StatusForbidden {
		t.Errorf("expected 403 when key missing, got %d", httpErr.Code)
	}
	if !errors.Is(httpErr.Internal, ErrSignedURLKeyMissing) {
		t.Errorf("expected Internal to wrap ErrSignedURLKeyMissing, got %v", httpErr.Internal)
	}
}

// TestSignedURL_HasValidSignatureKeyMissing pins the verify-only path
// behaviour: ValidateSignature returns ErrSignedURLKeyMissing so a
// caller using the helper directly (not via SignedMiddleware) can
// choose its own policy. HasValidSignature collapses that to false.
func TestSignedURL_HasValidSignatureKeyMissing(t *testing.T) {
	r := NewV2()
	r.Get("/orders/{id}", dummyHandler).Name("orders.show")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/orders/1", nil))

	req := httptest.NewRequest("GET", "/orders/1?expires=99999999999&signature=abc", nil)
	if err := r.ValidateSignature(req); !errors.Is(err, ErrSignedURLKeyMissing) {
		t.Fatalf("ValidateSignature expected ErrSignedURLKeyMissing, got %v", err)
	}
	if r.HasValidSignature(req) {
		t.Fatal("HasValidSignature must return false when key is missing")
	}
}

func TestSignedURL_DeriveKeyEmpty(t *testing.T) {
	if _, err := DeriveSignedURLKey(nil); !errors.Is(err, ErrSignedURLKeyMissing) {
		t.Fatalf("expected ErrSignedURLKeyMissing for nil master key, got %v", err)
	}
	if _, err := DeriveSignedURLKey([]byte{}); !errors.Is(err, ErrSignedURLKeyMissing) {
		t.Fatalf("expected ErrSignedURLKeyMissing for empty master key, got %v", err)
	}
}

func TestSignedURL_DeriveKeyDeterministic(t *testing.T) {
	master := []byte("super-secret-app-key-for-derivation")
	a, err := DeriveSignedURLKey(master)
	if err != nil {
		t.Fatalf("DeriveSignedURLKey: %v", err)
	}
	b, err := DeriveSignedURLKey(master)
	if err != nil {
		t.Fatalf("DeriveSignedURLKey: %v", err)
	}
	if len(a) != 32 {
		t.Errorf("expected 32-byte derived key, got %d", len(a))
	}
	// Same master, same info, so same key.
	if string(a) != string(b) {
		t.Error("HKDF derivation is not deterministic")
	}
	// Different master, so different key.
	other, err := DeriveSignedURLKey([]byte("different-master"))
	if err != nil {
		t.Fatalf("DeriveSignedURLKey: %v", err)
	}
	if string(a) == string(other) {
		t.Error("HKDF derivation did not vary with master key")
	}
}

func TestSignedURL_RouteNotFound(t *testing.T) {
	r := testRouterWithSignedKey(t)
	_, err := r.TemporarySignedURL("unknown", nil, nil, time.Hour)
	if err == nil {
		t.Fatal("expected error for unknown route")
	}
	if _, ok := err.(*RouteNotFoundError); !ok {
		t.Errorf("expected *RouteNotFoundError, got %T", err)
	}
}

func TestSignedURL_SetKeyDefensiveCopy(t *testing.T) {
	r := NewV2()
	r.Get("/orders/{id}", dummyHandler).Name("orders.show")
	req0 := httptest.NewRequest("GET", "/orders/1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req0)

	original := make([]byte, 32)
	for i := range original {
		original[i] = byte(i)
	}
	r.SetSignedURLKey(original)
	signed, err := r.TemporarySignedURL("orders.show", map[string]string{"id": "1"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	// Mutate the caller's slice. Must not affect the router-held key.
	for i := range original {
		original[i] = 0xFF
	}
	req := httptest.NewRequest("GET", signed, nil)
	if err := r.ValidateSignature(req); err != nil {
		t.Fatalf("expected key copy to remain intact after caller mutation, got %v", err)
	}
}

func TestSignedURL_SetKeyClearOnEmpty(t *testing.T) {
	r := testRouterWithSignedKey(t)

	r.SetSignedURLKey(nil)
	_, err := r.TemporarySignedURL("orders.show", map[string]string{"id": "1"}, nil, time.Hour)
	if !errors.Is(err, ErrSignedURLKeyMissing) {
		t.Fatalf("expected ErrSignedURLKeyMissing after key clear, got %v", err)
	}
}

// BenchmarkSignedURLValidate documents the verify hot path so operators
// can spot regressions in HMAC compare cost. We do not assert on timing
// (notoriously flaky in CI), only that the call succeeds repeatedly.
func BenchmarkSignedURLValidate(b *testing.B) {
	r := NewV2()
	r.Get("/orders/{id}", dummyHandler).Name("orders.show")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/orders/1", nil))
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	r.SetSignedURLKey(key)

	signed, err := r.TemporarySignedURL("orders.show", map[string]string{"id": "1"}, nil, time.Hour)
	if err != nil {
		b.Fatalf("SignedURL: %v", err)
	}
	req := httptest.NewRequest("GET", signed, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := r.ValidateSignature(req); err != nil {
			b.Fatal(err)
		}
	}
}
