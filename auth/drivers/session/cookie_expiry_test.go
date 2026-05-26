package session

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
)

// base64Encryptor is the pattern the existing cookie_test.go uses for "no
// real crypto, but make the payload cookie-safe via base64". We reuse it so
// the IssuedAt enforcement test does not depend on a real Encryptor.
func base64Encryptor() *mockEncryptor {
	return &mockEncryptor{
		encryptFunc: func(plaintext string) (string, error) {
			return base64.URLEncoding.EncodeToString([]byte(plaintext)), nil
		},
		decryptFunc: func(payload string) (string, error) {
			decoded, err := base64.URLEncoding.DecodeString(payload)
			if err != nil {
				return "", err
			}
			return string(decoded), nil
		},
	}
}

// TestCookieStore_RejectsExpiredCookie_ServerSide is the H-03 regression test.
// A captured cookie minted past its server-side lifetime MUST be rejected on
// Get even though the encryption + signature still validate; without the
// IssuedAt check the cookie remains replayable indefinitely.
func TestCookieStore_RejectsExpiredCookie_ServerSide(t *testing.T) {
	cfg := testConfig()
	cfg.Lifetime = 60 // minutes

	enc := base64Encryptor()
	store := newTestCookieStore(cfg, enc)

	// Mint a cookie payload IssuedAt two lifetimes ago.
	issued := time.Now().Add(-2 * time.Duration(cfg.Lifetime) * time.Minute)
	payload, err := json.Marshal(map[string]any{
		"id":    "expired-session-id",
		"data":  map[string]any{"user_id": "u-1"},
		"flash": map[string]any{},
		"iat":   issued,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	cookieValue := base64.URLEncoding.EncodeToString(payload)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: cfg.Name, Value: cookieValue})

	got, err := store.Get(req, "")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	// Expected: a fresh empty session (Create("") behaviour). The
	// original user_id MUST NOT survive into the returned session.
	if got.Get("user_id") != nil {
		t.Fatalf("expired cookie data leaked through Get: user_id=%v", got.Get("user_id"))
	}
	if got.ID() == "expired-session-id" {
		t.Fatalf("expired cookie session id reused; expected fresh id, got %q", got.ID())
	}
}

// TestCookieStore_AcceptsFreshCookie pins the happy path so the IssuedAt
// rejection does not over-fire on still-valid cookies.
func TestCookieStore_AcceptsFreshCookie(t *testing.T) {
	cfg := testConfig()
	cfg.Lifetime = 60

	enc := base64Encryptor()
	store := newTestCookieStore(cfg, enc)

	issued := time.Now().Add(-30 * time.Minute) // half a lifetime ago
	payload, err := json.Marshal(map[string]any{
		"id":    "fresh-session-id",
		"data":  map[string]any{"user_id": "u-2"},
		"flash": map[string]any{},
		"iat":   issued,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	cookieValue := base64.URLEncoding.EncodeToString(payload)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: cfg.Name, Value: cookieValue})

	got, err := store.Get(req, "")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID() != "fresh-session-id" {
		t.Fatalf("fresh cookie session id lost: want %q, got %q", "fresh-session-id", got.ID())
	}
	if got.Get("user_id") != "u-2" {
		t.Fatalf("fresh cookie data lost: want %q, got %v", "u-2", got.Get("user_id"))
	}
}

// TestCookieStore_AcceptsLegacyCookieWithoutIssuedAt covers the rolling-deploy
// path: cookies minted before the fix have no IssuedAt; they are accepted so
// existing sessions do not all 419 on deploy day. The next Save bumps the
// timestamp and from then on the cutoff enforces.
func TestCookieStore_AcceptsLegacyCookieWithoutIssuedAt(t *testing.T) {
	cfg := testConfig()
	cfg.Lifetime = 60

	enc := base64Encryptor()
	store := newTestCookieStore(cfg, enc)

	// No "iat" field at all (omitempty would have left it zero anyway).
	payload, err := json.Marshal(map[string]any{
		"id":    "legacy-session-id",
		"data":  map[string]any{"user_id": "u-3"},
		"flash": map[string]any{},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	cookieValue := base64.URLEncoding.EncodeToString(payload)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: cfg.Name, Value: cookieValue})

	got, err := store.Get(req, "")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID() != "legacy-session-id" {
		t.Fatalf("legacy cookie session id lost: want %q, got %q", "legacy-session-id", got.ID())
	}
}

// TestCookieStore_Save_BumpsIssuedAt confirms Save writes a non-zero IssuedAt
// so a subsequent Get can enforce the lifetime cutoff.
func TestCookieStore_Save_BumpsIssuedAt(t *testing.T) {
	cfg := testConfig()
	cfg.Lifetime = 60

	captured := ""
	enc := &mockEncryptor{
		encryptFunc: func(s string) (string, error) {
			captured = s
			return base64.URLEncoding.EncodeToString([]byte(s)), nil
		},
	}
	store := newTestCookieStore(cfg, enc)

	sess := &CookieSession{
		BaseSession: auth.NewSession("save-bumps-iat"),
		store:       store,
	}
	sess.Put("user_id", "u-4") // marks modified so Save actually writes

	rec := httptest.NewRecorder()
	if err := store.Save(rec, sess); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	var got struct {
		IssuedAt time.Time `json:"iat"`
	}
	if err := json.Unmarshal([]byte(captured), &got); err != nil {
		t.Fatalf("unmarshal captured payload: %v (payload=%q)", err, captured)
	}
	if got.IssuedAt.IsZero() {
		t.Fatalf("Save wrote zero IssuedAt; expected non-zero timestamp")
	}
	if time.Since(got.IssuedAt) > 5*time.Second {
		t.Fatalf("Save wrote IssuedAt %v, too far in the past", got.IssuedAt)
	}
}
