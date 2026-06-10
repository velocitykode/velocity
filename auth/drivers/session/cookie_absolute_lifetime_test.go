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

// V2-09 regression tests: the rolling IssuedAt window slides forward on
// every Save, so without an absolute cap a session kept warm by periodic
// requests never expires. These tests pin the CreatedAt-based absolute
// lifetime enforcement.

// fakeClock installs a controllable cookieNowFn and returns an advance
// function plus a restore function for defer.
func fakeClock(t *testing.T, start time.Time) func(d time.Duration) {
	t.Helper()
	current := start
	orig := cookieNowFn
	cookieNowFn = func() time.Time { return current }
	t.Cleanup(func() { cookieNowFn = orig })
	return func(d time.Duration) { current = current.Add(d) }
}

// mintCookieValue base64-encodes a raw payload map the way base64Encryptor
// expects, so tests can hand-craft legacy and aged payloads.
func mintCookieValue(t *testing.T, payload map[string]any) string {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return base64.URLEncoding.EncodeToString(data)
}

func requestWithCookie(cfg auth.SessionConfig, value string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: cfg.Name, Value: value})
	return req
}

// saveAndExtract saves the session and returns the resulting cookie value.
func saveAndExtract(t *testing.T, store *CookieStore, cfg auth.SessionConfig, sess auth.Session) string {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := store.Save(rec, sess); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == cfg.Name {
			return c.Value
		}
	}
	t.Fatalf("Save emitted no %q cookie", cfg.Name)
	return ""
}

// TestCookieStore_AbsoluteLifetime_FreshSessionPasses pins the happy path:
// a session well inside both windows loads normally.
func TestCookieStore_AbsoluteLifetime_FreshSessionPasses(t *testing.T) {
	cfg := testConfig()
	cfg.Lifetime = 60
	cfg.AbsoluteLifetime = 120

	start := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	fakeClock(t, start)

	store := newTestCookieStore(cfg, base64Encryptor())
	value := mintCookieValue(t, map[string]any{
		"id":    "fresh-abs",
		"data":  map[string]any{"user_id": "u-1"},
		"flash": map[string]any{},
		"iat":   start.Add(-10 * time.Minute),
		"cat":   start.Add(-30 * time.Minute),
	})

	got, err := store.Get(requestWithCookie(cfg, value), "")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID() != "fresh-abs" {
		t.Fatalf("fresh session rejected: want id %q, got %q", "fresh-abs", got.ID())
	}
	if got.Get("user_id") != "u-1" {
		t.Fatalf("fresh session data lost: got %v", got.Get("user_id"))
	}
}

// TestCookieStore_AbsoluteLifetime_WarmSessionDies is the core V2-09
// regression: a session kept alive by Saves more frequent than Lifetime
// still dies once CreatedAt + AbsoluteLifetime passes.
func TestCookieStore_AbsoluteLifetime_WarmSessionDies(t *testing.T) {
	cfg := testConfig()
	cfg.Lifetime = 60          // rolling window: 1h
	cfg.AbsoluteLifetime = 120 // absolute cap: 2h

	start := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	advance := fakeClock(t, start)

	store := newTestCookieStore(cfg, base64Encryptor())

	sess, err := store.Create("warm-session")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	sess.Put("user_id", "u-2")
	value := saveAndExtract(t, store, cfg, sess) // CreatedAt = start

	// Keep the session warm: every 45 minutes (inside the 60-minute
	// rolling window) Get + modify + Save. At +45 and +90 it must stay
	// alive; the Save at +90 slides IssuedAt to +90.
	for round := 0; round < 2; round++ {
		advance(45 * time.Minute)
		got, err := store.Get(requestWithCookie(cfg, value), "")
		if err != nil {
			t.Fatalf("Get returned error: %v", err)
		}
		if got.ID() != "warm-session" {
			t.Fatalf("warm session died early at %v: got id %q", cookieNowFn().Sub(start), got.ID())
		}
		got.Put("touch", round)
		value = saveAndExtract(t, store, cfg, got)
	}

	// +135 minutes total age. Rolling window alone would accept (last
	// IssuedAt is 45 minutes old), but the absolute cap (120) has passed.
	advance(45 * time.Minute)
	got, err := store.Get(requestWithCookie(cfg, value), "")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID() == "warm-session" {
		t.Fatalf("warm session survived past absolute lifetime: age %v, cap %v",
			cookieNowFn().Sub(start), time.Duration(cfg.AbsoluteLifetime)*time.Minute)
	}
	if got.Get("user_id") != nil {
		t.Fatalf("expired session data leaked: user_id=%v", got.Get("user_id"))
	}
}

// TestCookieStore_AbsoluteLifetime_LegacyPayloadFallsBackToIssuedAt covers
// pre-deploy cookies that carry iat but no cat: the absolute check anchors
// on IssuedAt, so an old-but-rolling-valid legacy cookie is still capped.
func TestCookieStore_AbsoluteLifetime_LegacyPayloadFallsBackToIssuedAt(t *testing.T) {
	cfg := testConfig()
	cfg.Lifetime = 0           // rolling enforcement off: isolates the absolute path
	cfg.AbsoluteLifetime = 600 // 10h absolute cap

	start := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	fakeClock(t, start)

	store := newTestCookieStore(cfg, base64Encryptor())
	value := mintCookieValue(t, map[string]any{
		"id":    "legacy-old",
		"data":  map[string]any{"user_id": "u-3"},
		"flash": map[string]any{},
		"iat":   start.Add(-11 * time.Hour), // no cat: absolute anchors on iat, 11h > 10h cap
	})

	got, err := store.Get(requestWithCookie(cfg, value), "")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID() == "legacy-old" {
		t.Fatalf("legacy cookie past absolute lifetime accepted via IssuedAt fallback gap")
	}
}

// TestCookieStore_AbsoluteLifetime_LegacyPayloadStampedOnSave: a still-valid
// legacy cookie (iat only) loads, and the next Save persists cat equal to
// the original iat, not the current time.
func TestCookieStore_AbsoluteLifetime_LegacyPayloadStampedOnSave(t *testing.T) {
	cfg := testConfig()
	cfg.Lifetime = 120
	cfg.AbsoluteLifetime = 1440

	start := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	fakeClock(t, start)

	store := newTestCookieStore(cfg, base64Encryptor())

	issued := start.Add(-1 * time.Hour)
	value := mintCookieValue(t, map[string]any{
		"id":    "legacy-live",
		"data":  map[string]any{"user_id": "u-4"},
		"flash": map[string]any{},
		"iat":   issued,
	})

	got, err := store.Get(requestWithCookie(cfg, value), "")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID() != "legacy-live" {
		t.Fatalf("live legacy cookie rejected: got id %q", got.ID())
	}

	got.Put("touch", true)
	newValue := saveAndExtract(t, store, cfg, got)

	decoded, err := base64.URLEncoding.DecodeString(newValue)
	if err != nil {
		t.Fatalf("decode saved cookie: %v", err)
	}
	var persisted struct {
		CreatedAt time.Time `json:"cat"`
		IssuedAt  time.Time `json:"iat"`
	}
	if err := json.Unmarshal(decoded, &persisted); err != nil {
		t.Fatalf("unmarshal saved payload: %v", err)
	}
	if !persisted.CreatedAt.Equal(issued) {
		t.Fatalf("legacy CreatedAt not anchored to original IssuedAt: want %v, got %v", issued, persisted.CreatedAt)
	}
	if !persisted.IssuedAt.Equal(start) {
		t.Fatalf("Save did not bump IssuedAt to now: want %v, got %v", start, persisted.IssuedAt)
	}
}

// TestCookieStore_AbsoluteLifetime_CreatedAtSurvivesRoundTrips: cat is
// copied forward verbatim through repeated Save cycles while iat bumps.
func TestCookieStore_AbsoluteLifetime_CreatedAtSurvivesRoundTrips(t *testing.T) {
	cfg := testConfig()
	cfg.Lifetime = 120
	cfg.AbsoluteLifetime = 14400 // 10 days, far away

	start := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	advance := fakeClock(t, start)

	store := newTestCookieStore(cfg, base64Encryptor())

	created := start.Add(-2 * time.Hour)
	value := mintCookieValue(t, map[string]any{
		"id":    "roundtrip",
		"data":  map[string]any{},
		"flash": map[string]any{},
		"iat":   start.Add(-5 * time.Minute),
		"cat":   created,
	})

	for round := 0; round < 3; round++ {
		got, err := store.Get(requestWithCookie(cfg, value), "")
		if err != nil {
			t.Fatalf("round %d: Get returned error: %v", round, err)
		}
		if got.ID() != "roundtrip" {
			t.Fatalf("round %d: session rejected unexpectedly", round)
		}
		got.Put("round", round)
		value = saveAndExtract(t, store, cfg, got)

		decoded, err := base64.URLEncoding.DecodeString(value)
		if err != nil {
			t.Fatalf("round %d: decode cookie: %v", round, err)
		}
		var persisted struct {
			CreatedAt time.Time `json:"cat"`
		}
		if err := json.Unmarshal(decoded, &persisted); err != nil {
			t.Fatalf("round %d: unmarshal payload: %v", round, err)
		}
		if !persisted.CreatedAt.Equal(created) {
			t.Fatalf("round %d: CreatedAt drifted: want %v, got %v", round, created, persisted.CreatedAt)
		}
		advance(30 * time.Minute)
	}
}

// TestCookieStore_AbsoluteLifetime_ZeroConfigUsesDefault: AbsoluteLifetime
// left at zero still enforces the 30-day default (fail-secure, not
// disabled).
func TestCookieStore_AbsoluteLifetime_ZeroConfigUsesDefault(t *testing.T) {
	cfg := testConfig()
	cfg.Lifetime = 0 // no rolling enforcement, isolates the absolute check
	cfg.AbsoluteLifetime = 0

	start := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	fakeClock(t, start)

	store := newTestCookieStore(cfg, base64Encryptor())

	mint := func(id string, age time.Duration) string {
		return mintCookieValue(t, map[string]any{
			"id":    id,
			"data":  map[string]any{},
			"flash": map[string]any{},
			"iat":   start.Add(-time.Minute),
			"cat":   start.Add(-age),
		})
	}

	// 31 days old: past the 30-day default, rejected.
	got, err := store.Get(requestWithCookie(cfg, mint("too-old", 31*24*time.Hour)), "")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID() == "too-old" {
		t.Fatalf("zero AbsoluteLifetime did not apply the 30-day default")
	}

	// 29 days old: inside the default, accepted.
	got, err = store.Get(requestWithCookie(cfg, mint("still-ok", 29*24*time.Hour)), "")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID() != "still-ok" {
		t.Fatalf("default absolute lifetime over-fired on a 29-day session")
	}
}

// TestCookieStore_AbsoluteLifetime_NegativeDisables: the documented opt-out
// sentinel removes the absolute cap entirely.
func TestCookieStore_AbsoluteLifetime_NegativeDisables(t *testing.T) {
	cfg := testConfig()
	cfg.Lifetime = 0
	cfg.AbsoluteLifetime = -1

	start := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	fakeClock(t, start)

	store := newTestCookieStore(cfg, base64Encryptor())
	value := mintCookieValue(t, map[string]any{
		"id":    "immortal",
		"data":  map[string]any{},
		"flash": map[string]any{},
		"iat":   start.Add(-time.Minute),
		"cat":   start.Add(-400 * 24 * time.Hour),
	})

	got, err := store.Get(requestWithCookie(cfg, value), "")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.ID() != "immortal" {
		t.Fatalf("negative AbsoluteLifetime should disable the cap; session rejected")
	}
}
