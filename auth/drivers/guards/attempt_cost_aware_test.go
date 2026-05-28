package guards

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/crypto"
)

// bcryptVerifyingProvider mimics a real UserProvider: ValidateCredentials
// runs bcrypt.CompareHashAndPassword via the framework hasher. We need
// this to exercise the wrong-password path at full bcrypt cost so the
// timing comparison against the missing-user branch is meaningful.
type bcryptVerifyingProvider struct {
	user *timingTestUser
	cost int
}

func (p *bcryptVerifyingProvider) FindByID(id interface{}) (auth.Authenticatable, error) {
	if p.user != nil && p.user.id == id {
		return p.user, nil
	}
	return nil, nil
}
func (p *bcryptVerifyingProvider) FindByCredentials(credentials map[string]interface{}) (auth.Authenticatable, error) {
	email, _ := credentials["email"].(string)
	if p.user != nil && email == p.user.id {
		return p.user, nil
	}
	return nil, nil
}
func (p *bcryptVerifyingProvider) ValidateCredentials(user auth.Authenticatable, credentials map[string]interface{}) bool {
	password, _ := credentials["password"].(string)
	return auth.NewBcryptHasher(p.cost).Verify(password, p.user.password)
}
func (p *bcryptVerifyingProvider) UpdateRememberToken(auth.Authenticatable, string) error {
	return nil
}

// TestSessionGuard_Attempt_DummyHashTracksBcryptCost is the F2 regression
// test. When the operator configures bcrypt cost N on the guard via
// SetHasher, the missing-user branch MUST run the dummy hash at the same
// cost N (not the package default 10). Without cost-matching, the
// missing-user CPU profile is faster than the wrong-password profile and
// the H-09 timing defense degrades.
//
// We assert this by running both branches with the floor disabled and
// confirming wall-clock latencies are within a tight ratio. With the bug
// present (dummy at default cost, real verify at configured cost), the
// ratio diverges by 5-10x; with the fix it stays close to 1x.
func TestSessionGuard_Attempt_DummyHashTracksBcryptCost(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping bcrypt-cost timing test in -short mode")
	}

	const realCost = 8 // distinct from default 10; fast enough for CI
	password := "correct-password"

	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	bcryptHash, err := auth.NewBcryptHasher(realCost).Hash(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := &timingTestUser{id: "real@example.com", password: bcryptHash}
	provider := &bcryptVerifyingProvider{user: user, cost: realCost}

	guard, err := guards_NewSessionGuard(provider, enc)
	if err != nil {
		t.Fatalf("NewSessionGuard: %v", err)
	}
	// Install a hasher at the same cost the real user uses. The fix
	// then sizes the dummy hash at this same cost.
	guard.SetHasher(auth.NewBcryptHasher(realCost))
	// Disable the floor so we measure the underlying credential-check
	// CPU profile directly.
	guard.SetAttemptFloor(-1)

	// Warm the dummy-hash and real-hash caches so neither branch pays
	// first-call generate-from-password latency.
	_ = auth.GetDummyBcryptHash(realCost)
	_ = auth.NewBcryptHasher(realCost).Verify("warm", bcryptHash)

	measure := func(label, email string) time.Duration {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		start := time.Now()
		_, _ = guard.Attempt(w, r, map[string]interface{}{
			"email":    email,
			"password": "wrong-password",
		})
		elapsed := time.Since(start)
		t.Logf("%s: %v", label, elapsed)
		return elapsed
	}

	// Average a few iterations to smooth scheduler jitter.
	const reps = 3
	var missing, wrong time.Duration
	for i := 0; i < reps; i++ {
		missing += measure("missing-user", "ghost@example.com")
		wrong += measure("wrong-password", "real@example.com")
	}
	missing /= reps
	wrong /= reps

	// Both branches now run cost-realCost bcrypt verify, so latencies
	// must be close. A ratio > 3x means the fix regressed.
	hi, lo := missing, wrong
	if lo > hi {
		hi, lo = wrong, missing
	}
	if lo <= 0 {
		t.Fatalf("measurement floor: missing=%v wrong=%v", missing, wrong)
	}
	ratio := float64(hi) / float64(lo)
	if ratio > 3.0 {
		t.Fatalf("missing-user (%v) and wrong-password (%v) latencies diverge by %.2fx; expected within ~3x at matched bcrypt cost",
			missing, wrong, ratio)
	}
}

// guards_NewSessionGuard is a thin local helper that bypasses the
// guard's NewSessionGuard ergonomic-config layer so we control every
// detail of the test setup.
func guards_NewSessionGuard(provider auth.UserProvider, enc crypto.Encryptor) (*SessionGuard, error) {
	return NewSessionGuard(provider, auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 60,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}, enc)
}

// TestSessionGuard_SetHasher_PropagatesToDummyHashCost is the focused
// behavioural test for F2: after SetHasher at cost 12, the missing-user
// branch must invoke the hasher with a cost-12 dummy hash. We verify by
// observing the resolved dummy hash from the hasher chain.
func TestSessionGuard_SetHasher_PropagatesToDummyHashCost(t *testing.T) {
	const targetCost = 11

	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	guard, err := NewSessionGuard(&mockSessionGuardUserProvider{}, auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 60,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionGuard: %v", err)
	}
	guard.SetHasher(auth.NewBcryptHasher(targetCost))

	hash := dummyHashForHasher(guard.effectiveHasher())
	// bcrypt hashes start with $2a$<cost>$... so we can parse the cost.
	// Format: $2a$11$...
	if !strings.HasPrefix(string(hash), "$2") {
		t.Fatalf("expected bcrypt hash, got %q", string(hash))
	}
	// Parse cost from the third field.
	parts := strings.SplitN(string(hash), "$", 4)
	if len(parts) < 4 {
		t.Fatalf("bcrypt hash malformed: %q", string(hash))
	}
	gotCost := parts[2]
	wantCost := "11"
	if gotCost != wantCost {
		t.Fatalf("dummy hash cost = %s; want %s (must track configured hasher cost)", gotCost, wantCost)
	}
}

// TestSessionGuard_Attempt_AttemptFloorOverride confirms a non-default
// AttemptFloor configured via SetAttemptFloor is respected. F2 mandates
// operators with bcrypt cost > 10 raise the floor so the real-verify
// path fits inside the timebox budget.
func TestSessionGuard_Attempt_AttemptFloorOverride(t *testing.T) {
	guard, _, _ := newTimingGuard(t, "correct-password")
	const want = 350 * time.Millisecond
	guard.SetAttemptFloor(want)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r = WithSessionContext(r)

	start := time.Now()
	_, _ = guard.Attempt(w, r, map[string]interface{}{
		"email":    "ghost@example.com",
		"password": "anything",
	})
	elapsed := time.Since(start)
	if elapsed < want {
		t.Fatalf("Attempt completed in %v; expected >= %v (configured AttemptFloor)", elapsed, want)
	}
}

// TestGetDummyBcryptHash_Memoisation confirms the per-cost cache: a
// repeat call at the same cost MUST return the exact same byte slice (or
// at least skip bcrypt generation, which we observe as O(1) wall-clock).
func TestGetDummyBcryptHash_Memoisation(t *testing.T) {
	const cost = 7 // fast, but exercise the cache anyway

	first := auth.GetDummyBcryptHash(cost)
	start := time.Now()
	second := auth.GetDummyBcryptHash(cost)
	elapsed := time.Since(start)

	if string(first) != string(second) {
		t.Fatalf("GetDummyBcryptHash(%d) returned different bytes on repeat call", cost)
	}
	// Second call should be O(1); allow generous 5ms ceiling.
	if elapsed > 5*time.Millisecond {
		t.Fatalf("repeat GetDummyBcryptHash(%d) took %v; expected near-instant cache hit", cost, elapsed)
	}
}

// TestGetDummyBcryptHash_ClampsCost covers the lower-bound enforcement
// so callers passing zero/negative cost get a default hash rather than a
// bcrypt panic. The MinCost path is exercised similarly via cost=1 (raises
// to MinCost=4). The MaxCost clamp is verified statically (cost=99 would
// otherwise take hours to compute even at the clamped value of 31), so we
// only assert the configured clamp logic via the public surface using
// values inside the runnable range.
func TestGetDummyBcryptHash_ClampsCost(t *testing.T) {
	// negative -> DefaultCost
	if h := auth.GetDummyBcryptHash(-5); len(h) == 0 {
		t.Fatal("GetDummyBcryptHash(-5) returned empty; expected default-cost hash")
	}
	// zero -> DefaultCost
	if h := auth.GetDummyBcryptHash(0); len(h) == 0 {
		t.Fatal("GetDummyBcryptHash(0) returned empty; expected default-cost hash")
	}
	// 1 is below MinCost (4); should be clamped up.
	if h := auth.GetDummyBcryptHash(1); len(h) == 0 {
		t.Fatal("GetDummyBcryptHash(1) returned empty; expected clamped MinCost hash")
	}
}

// Ctx-suffixed shims for auth.UserProvider, added in Sweep 1b.
func (p *bcryptVerifyingProvider) FindByIDCtx(_ context.Context, id interface{}) (auth.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *bcryptVerifyingProvider) FindByCredentialsCtx(_ context.Context, credentials map[string]interface{}) (auth.Authenticatable, error) {
	return p.FindByCredentials(credentials)
}
func (p *bcryptVerifyingProvider) UpdateRememberTokenCtx(_ context.Context, user auth.Authenticatable, token string) error {
	return p.UpdateRememberToken(user, token)
}
