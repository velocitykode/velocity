package session

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCookieStore_RevokedSessionRejectedOnGet is the H-04 regression test.
// After Revoke is called for a session id, a captured cookie carrying that
// id MUST NOT authenticate even though decryption and IssuedAt enforcement
// would otherwise pass.
func TestCookieStore_RevokedSessionRejectedOnGet(t *testing.T) {
	cfg := testConfig()
	cfg.Lifetime = 60

	enc := base64Encryptor()
	store := newTestCookieStore(cfg, enc)

	issued := time.Now().Add(-1 * time.Minute) // fresh, still inside lifetime
	payload, err := json.Marshal(map[string]any{
		"id":    "revoked-session-id",
		"data":  map[string]any{"user_id": "u-stale"},
		"flash": map[string]any{},
		"iat":   issued,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	cookieValue := base64.URLEncoding.EncodeToString(payload)

	// Sanity: BEFORE revocation, the cookie authenticates as expected.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: cfg.Name, Value: cookieValue})
	sess, err := store.Get(req, "")
	if err != nil {
		t.Fatalf("Get returned error pre-revoke: %v", err)
	}
	if sess.ID() != "revoked-session-id" || sess.Get("user_id") != "u-stale" {
		t.Fatalf("pre-revoke Get unexpectedly dropped session data: id=%q user_id=%v",
			sess.ID(), sess.Get("user_id"))
	}

	// Revoke. A captured-cookie replay MUST now fail.
	store.Revoke("revoked-session-id")

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(&http.Cookie{Name: cfg.Name, Value: cookieValue})
	sess2, err := store.Get(req2, "")
	if err != nil {
		t.Fatalf("Get returned error post-revoke: %v", err)
	}
	if sess2.ID() == "revoked-session-id" {
		t.Fatalf("Get returned revoked session id %q; expected fresh empty session", sess2.ID())
	}
	if sess2.Get("user_id") != nil {
		t.Fatalf("Get returned revoked session user_id %v; expected fresh empty session", sess2.Get("user_id"))
	}
}

// TestCookieStore_Revoke_AgesOutExpiredEntries pins the cleanup behaviour so
// the in-memory revocation map does not grow without bound.
func TestCookieStore_Revoke_AgesOutExpiredEntries(t *testing.T) {
	cfg := testConfig()
	cfg.Lifetime = 1 // 1 minute lifetime

	enc := base64Encryptor()
	store := newTestCookieStore(cfg, enc)

	// Inject an "old" revoked entry whose TTL is already in the past.
	store.revoked["old-id"] = time.Now().Add(-10 * time.Minute)
	store.revokedTTLs["old-id"] = time.Now().Add(-5 * time.Minute)

	// A new Revoke triggers the opportunistic cleanup pass.
	store.Revoke("fresh-id")

	store.revokedMu.RLock()
	defer store.revokedMu.RUnlock()
	if _, ok := store.revoked["old-id"]; ok {
		t.Fatalf("expected old-id to be swept on Revoke; still present")
	}
	if _, ok := store.revokedTTLs["fresh-id"]; !ok {
		t.Fatalf("expected fresh-id to be tracked")
	}
}

// TestCookieStore_RevokeEmptyIDIsNoop covers the defensive branch in Revoke.
func TestCookieStore_RevokeEmptyIDIsNoop(t *testing.T) {
	cfg := testConfig()
	enc := base64Encryptor()
	store := newTestCookieStore(cfg, enc)

	store.Revoke("")

	store.revokedMu.RLock()
	defer store.revokedMu.RUnlock()
	if len(store.revoked) != 0 || len(store.revokedTTLs) != 0 {
		t.Fatalf("Revoke(\"\") populated maps; expected no-op")
	}
}

// TestCookieStore_NewCookieStorePopulatesRevocationMaps is a smoke check that
// the constructor initialises the maps so a no-config caller of Revoke does
// not nil-deref.
func TestCookieStore_NewCookieStorePopulatesRevocationMaps(t *testing.T) {
	cfg := testConfig()
	enc := base64Encryptor()

	store, err := NewCookieStore(cfg, enc)
	if err != nil {
		t.Fatalf("NewCookieStore returned error: %v", err)
	}
	if store.revoked == nil || store.revokedTTLs == nil {
		t.Fatalf("NewCookieStore left revocation maps nil; expected initialised")
	}
	// Should not panic.
	store.Revoke("sanity")
	if !store.isRevoked("sanity") {
		t.Fatalf("isRevoked(sanity) = false; expected true after Revoke")
	}
}
