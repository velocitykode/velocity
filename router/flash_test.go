package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/crypto"
)

// newFlashEncryptor returns a fresh AES-256-GCM encryptor backed by a
// known 32-byte key. The test key is hard-coded so the seal/open round
// trip is deterministic across runs.
func newFlashEncryptor(t *testing.T) crypto.Encryptor {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    "base64:MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("newFlashEncryptor: %v", err)
	}
	return enc
}

// newCBCEncryptor exercises the CBC fallback path inside SealFlash.
// CBC ciphers reject EncryptBytesWithAAD; the sealer must degrade to
// EncryptBytes so apps pinned to CBC still get authenticated cookies.
func newCBCEncryptor(t *testing.T) crypto.Encryptor {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    "base64:MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		Cipher: "AES-256-CBC",
	})
	if err != nil {
		t.Fatalf("newCBCEncryptor: %v", err)
	}
	return enc
}

func TestSealOpenFlash_Roundtrip(t *testing.T) {
	enc := newFlashEncryptor(t)
	input := map[string]any{"email": "required", "name": "required"}

	sealed, err := SealFlash(enc, FlashErrorsCookie, input)
	if err != nil {
		t.Fatalf("SealFlash: %v", err)
	}
	if sealed == "" {
		t.Fatal("SealFlash returned empty payload")
	}

	out, err := OpenFlash(enc, FlashErrorsCookie, sealed)
	if err != nil {
		t.Fatalf("OpenFlash: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", out)
	}
	if m["email"] != "required" || m["name"] != "required" {
		t.Errorf("roundtripped payload mismatch: %v", m)
	}
}

func TestSealOpenFlash_RoundtripCBCFallback(t *testing.T) {
	enc := newCBCEncryptor(t)
	input := map[string]any{"k": "v"}

	sealed, err := SealFlash(enc, FlashErrorsCookie, input)
	if err != nil {
		t.Fatalf("SealFlash on CBC: %v", err)
	}
	out, err := OpenFlash(enc, FlashErrorsCookie, sealed)
	if err != nil {
		t.Fatalf("OpenFlash on CBC: %v", err)
	}
	m, _ := out.(map[string]any)
	if m["k"] != "v" {
		t.Errorf("CBC fallback roundtrip lost data: %v", out)
	}
}

func TestSealFlash_NilEncryptorErrors(t *testing.T) {
	_, err := SealFlash(nil, FlashErrorsCookie, "x")
	if err == nil {
		t.Fatal("SealFlash with nil encryptor should error")
	}
}

func TestOpenFlash_NilEncryptorErrors(t *testing.T) {
	_, err := OpenFlash(nil, FlashErrorsCookie, "x")
	if err == nil {
		t.Fatal("OpenFlash with nil encryptor should error")
	}
}

func TestSealFlash_UnknownNameErrors(t *testing.T) {
	enc := newFlashEncryptor(t)
	_, err := SealFlash(enc, "_velocity_bogus", "x")
	if err == nil {
		t.Fatal("SealFlash with unknown name should error")
	}
}

func TestOpenFlash_UnknownNameErrors(t *testing.T) {
	enc := newFlashEncryptor(t)
	_, err := OpenFlash(enc, "_velocity_bogus", "x")
	if err == nil {
		t.Fatal("OpenFlash with unknown name should error")
	}
}

func TestOpenFlash_AADCrossBindingRejected(t *testing.T) {
	enc := newFlashEncryptor(t)
	sealed, err := SealFlash(enc, FlashErrorsCookie, map[string]any{"email": "required"})
	if err != nil {
		t.Fatalf("SealFlash: %v", err)
	}
	// Present the errors-bound ciphertext under the input cookie name.
	_, err = OpenFlash(enc, FlashInputCookie, sealed)
	if err == nil {
		t.Fatal("OpenFlash must reject AAD cross-binding")
	}
}

func TestOpenFlash_OversizedRejected(t *testing.T) {
	enc := newFlashEncryptor(t)
	huge := strings.Repeat("A", MaxFlashCookieSize+1)
	_, err := OpenFlash(enc, FlashErrorsCookie, huge)
	if err == nil {
		t.Fatal("OpenFlash must reject oversized cookies")
	}
}

func TestOpenFlash_TamperedRejected(t *testing.T) {
	enc := newFlashEncryptor(t)
	sealed, err := SealFlash(enc, FlashErrorsCookie, map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("SealFlash: %v", err)
	}
	// Flip the trailing character to corrupt the auth tag. The result
	// must NOT decrypt.
	if len(sealed) < 2 {
		t.Fatalf("sealed payload too short: %q", sealed)
	}
	last := sealed[len(sealed)-1]
	flipped := "A"
	if last == 'A' {
		flipped = "B"
	}
	tampered := sealed[:len(sealed)-1] + flipped
	_, err = OpenFlash(enc, FlashErrorsCookie, tampered)
	if err == nil {
		t.Fatal("OpenFlash must reject tampered ciphertext")
	}
}

func TestOpenFlash_WrongKeyRejected(t *testing.T) {
	encA := newFlashEncryptor(t)
	encB, err := crypto.NewEncryptor(crypto.Config{
		Key:    "base64:YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE=",
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("second encryptor: %v", err)
	}
	sealed, err := SealFlash(encA, FlashErrorsCookie, map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("SealFlash: %v", err)
	}
	_, err = OpenFlash(encB, FlashErrorsCookie, sealed)
	if err == nil {
		t.Fatal("OpenFlash must reject ciphertext from a different key")
	}
}

func TestOpenFlash_EmptyValueRejected(t *testing.T) {
	enc := newFlashEncryptor(t)
	_, err := OpenFlash(enc, FlashErrorsCookie, "")
	if err == nil {
		t.Fatal("OpenFlash must reject empty cookie value")
	}
}

func TestContextWithErrors_SetsAuthenticatedCookie(t *testing.T) {
	enc := newFlashEncryptor(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	c := NewContext(w, r)
	c.SetServices(&app.Services{Crypto: enc})

	c.WithErrors(map[string]any{"email": "required"})

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 Set-Cookie, got %d", len(cookies))
	}
	got := cookies[0]
	if got.Name != FlashErrorsCookie {
		t.Errorf("cookie name = %q, want %q", got.Name, FlashErrorsCookie)
	}
	if got.Value == "" {
		t.Error("cookie value must not be empty")
	}
	if !got.HttpOnly || !got.Secure || got.SameSite != http.SameSiteLaxMode || got.Path != "/" || got.MaxAge != 300 {
		t.Errorf("cookie attributes wrong: %#v", got)
	}

	// The emitted cookie must decode back to the original payload.
	out, err := OpenFlash(enc, FlashErrorsCookie, got.Value)
	if err != nil {
		t.Fatalf("OpenFlash on emitted cookie: %v", err)
	}
	if m, _ := out.(map[string]any); m["email"] != "required" {
		t.Errorf("decoded cookie payload mismatch: %v", out)
	}
}

func TestContextWithErrors_NoEncryptorIsSilentNoop(t *testing.T) {
	// When the app has no Crypto wired (e.g. test contexts), the write
	// must NOT emit a plaintext cookie. Silent no-op is the correct
	// fail-safe.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	c := NewContext(w, r)
	c.SetServices(&app.Services{}) // Crypto is nil

	c.WithErrors(map[string]any{"email": "required"})

	cookies := w.Result().Cookies()
	if len(cookies) != 0 {
		t.Errorf("expected no cookies without encryptor, got %d: %#v", len(cookies), cookies)
	}
}

func TestContextWithErrors_NoServicesIsSilentNoop(t *testing.T) {
	// Raw NewContext with no SetServices: also a fail-safe no-op.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	c := NewContext(w, r)

	c.WithErrors(map[string]any{"k": "v"})

	if cookies := w.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("expected no cookies without services, got %d", len(cookies))
	}
}

func TestContextWithInput_SetsAuthenticatedCookie(t *testing.T) {
	enc := newFlashEncryptor(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	c := NewContext(w, r)
	c.SetServices(&app.Services{Crypto: enc})

	c.WithInput(map[string]any{"email": "bad@"})

	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != FlashInputCookie {
		t.Fatalf("expected single %s cookie, got %#v", FlashInputCookie, cookies)
	}
	if _, err := OpenFlash(enc, FlashInputCookie, cookies[0].Value); err != nil {
		t.Errorf("emitted input cookie does not decode: %v", err)
	}
}

func TestSealOpenFlash_DistinctNamesProduceIncompatiblePayloads(t *testing.T) {
	// Domain separation: the same plaintext sealed under two different
	// flash names must yield ciphertexts that cannot be substituted
	// for one another. (GCM AAD enforces this; CBC fallback does not
	// but is documented as such.)
	enc := newFlashEncryptor(t)
	plain := map[string]any{"v": 1}

	a, err := SealFlash(enc, FlashErrorsCookie, plain)
	if err != nil {
		t.Fatalf("SealFlash A: %v", err)
	}
	// Try to open A's ciphertext under the wrong name.
	if _, err := OpenFlash(enc, FlashInputCookie, a); err == nil {
		t.Error("ciphertext sealed under errors must not open under input")
	}

	b, err := SealFlash(enc, FlashInputCookie, plain)
	if err != nil {
		t.Fatalf("SealFlash B: %v", err)
	}
	if _, err := OpenFlash(enc, FlashErrorsCookie, b); err == nil {
		t.Error("ciphertext sealed under input must not open under errors")
	}
}

// TestSealFlash_PropagatesNonCipherErrors guards the fallback path so it
// only kicks in for ErrInvalidCipher. Other errors (e.g. a marshal
// failure on the plaintext) must surface to the caller without being
// silently swallowed.
func TestSealFlash_PropagatesNonCipherErrors(t *testing.T) {
	enc := newFlashEncryptor(t)
	_, err := SealFlash(enc, FlashErrorsCookie, make(chan int))
	if err == nil {
		t.Fatal("SealFlash must surface marshal failures")
	}
	// json package wraps unsupported-type errors; checking we got
	// *something* non-nil and not a generic ErrInvalidCipher is enough.
	if errors.Is(err, crypto.ErrInvalidCipher) {
		t.Errorf("did not expect ErrInvalidCipher, got %v", err)
	}
}

func TestServicesFromRequest_Roundtrip(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := ServicesFromRequest(r); got != nil {
		t.Errorf("bare request must have no services, got %#v", got)
	}
	want := &app.Services{}
	r = WithServices(r, want)
	if got := ServicesFromRequest(r); got != want {
		t.Errorf("ServicesFromRequest returned %#v, want %#v", got, want)
	}
}

func TestServicesFromRequest_NilRequest(t *testing.T) {
	if got := ServicesFromRequest(nil); got != nil {
		t.Errorf("ServicesFromRequest(nil) = %#v, want nil", got)
	}
}
