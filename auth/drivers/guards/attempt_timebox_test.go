package guards

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/crypto"
)

// timingTestUser is a stand-in Authenticatable for the attempt-timing
// regression tests.
type timingTestUser struct {
	id       string
	password string
}

func (u *timingTestUser) GetAuthIdentifier() interface{} { return u.id }
func (u *timingTestUser) GetAuthPassword() string        { return u.password }
func (u *timingTestUser) GetRememberToken() string       { return "" }
func (u *timingTestUser) SetRememberToken(string)        {}

// hasherCallCounter wraps an auth.Hasher and increments a counter every
// time Verify is invoked. Used to confirm the dummy bcrypt path actually
// runs on the missing-user branch.
type hasherCallCounter struct {
	inner    auth.Hasher
	verifies atomic.Int32
}

func (h *hasherCallCounter) Hash(password string) (string, error) { return h.inner.Hash(password) }
func (h *hasherCallCounter) Verify(password, hash string) bool {
	h.verifies.Add(1)
	return h.inner.Verify(password, hash)
}
func (h *hasherCallCounter) NeedsRehash(hash string) bool { return h.inner.NeedsRehash(hash) }

// timingTestProvider implements auth.UserProvider with toggleable behavior
// so the test can simulate "user not found" and "user found, password
// wrong" without database plumbing.
type timingTestProvider struct {
	user *timingTestUser
}

func (p *timingTestProvider) FindByID(id interface{}) (auth.Authenticatable, error) {
	if p.user != nil && p.user.id == id {
		return p.user, nil
	}
	return nil, errors.New("not found")
}
func (p *timingTestProvider) FindByCredentials(credentials map[string]interface{}) (auth.Authenticatable, error) {
	email, _ := credentials["email"].(string)
	if p.user != nil && email == p.user.id {
		return p.user, nil
	}
	return nil, errors.New("not found")
}
func (p *timingTestProvider) ValidateCredentials(user auth.Authenticatable, credentials map[string]interface{}) bool {
	password, _ := credentials["password"].(string)
	return password == p.user.password
}
func (p *timingTestProvider) UpdateRememberToken(auth.Authenticatable, string) error { return nil }

// newTimingGuard builds a SessionGuard backed by an in-memory provider
// and a hasher whose Verify calls are observable.
func newTimingGuard(t *testing.T, password string) (*SessionGuard, *hasherCallCounter, *timingTestProvider) {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	bcryptHash, err := auth.NewBcryptHasher(4).Hash(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := &timingTestUser{id: "real@example.com", password: bcryptHash}
	provider := &timingTestProvider{user: user}
	guard, err := NewSessionGuard(provider, auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 60,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionGuard: %v", err)
	}
	counter := &hasherCallCounter{inner: auth.NewBcryptHasher(4)}
	guard.hasher = counter
	return guard, counter, provider
}

// TestSessionGuard_Attempt_MissingUserStillCallsHasher confirms the H-09
// dummy-bcrypt mitigation: when the user does not exist, Attempt MUST still
// run the configured hasher (against the dummy hash) so the CPU profile
// matches the bcrypt-verify branch.
func TestSessionGuard_Attempt_MissingUserStillCallsHasher(t *testing.T) {
	guard, counter, _ := newTimingGuard(t, "correct-password")
	// Bypass the wall-clock floor for this test; we only care that the
	// hasher fired, not how long the call took.
	guard.SetAttemptFloor(-1)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r = WithSessionContext(r)

	ok, err := guard.Attempt(w, r, map[string]interface{}{
		"email":    "ghost@example.com", // not in provider
		"password": "anything",
	})
	if ok {
		t.Fatal("Attempt returned true for unknown user")
	}
	if err != nil {
		t.Fatalf("Attempt returned err=%v for unknown user; expected nil", err)
	}
	if counter.verifies.Load() == 0 {
		t.Fatal("hasher.Verify was not invoked on missing-user path; expected dummy-bcrypt CPU match")
	}
}

// TestSessionGuard_Attempt_EnforcesFloor pins the wall-clock floor: with a
// configured floor of 50ms, the missing-user path MUST take at least 50ms.
func TestSessionGuard_Attempt_EnforcesFloor(t *testing.T) {
	guard, _, _ := newTimingGuard(t, "correct-password")
	guard.SetAttemptFloor(50 * time.Millisecond)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r = WithSessionContext(r)

	start := time.Now()
	_, err := guard.Attempt(w, r, map[string]interface{}{
		"email":    "ghost@example.com",
		"password": "anything",
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Attempt returned err=%v", err)
	}
	if elapsed < 50*time.Millisecond {
		t.Fatalf("Attempt completed in %v; expected >= 50ms wall-clock floor", elapsed)
	}
}

// TestSessionGuard_Attempt_DefaultFloorIsApplied confirms the package
// default of 200ms kicks in when SetAttemptFloor was never called.
func TestSessionGuard_Attempt_DefaultFloorIsApplied(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 200ms wall-clock test in -short mode")
	}
	guard, _, _ := newTimingGuard(t, "correct-password")
	// Do NOT call SetAttemptFloor: rely on the package default.

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r = WithSessionContext(r)

	start := time.Now()
	_, _ = guard.Attempt(w, r, map[string]interface{}{
		"email":    "ghost@example.com",
		"password": "anything",
	})
	elapsed := time.Since(start)
	if elapsed < auth.DefaultAttemptFloor {
		t.Fatalf("Attempt completed in %v; expected >= %v (DefaultAttemptFloor)", elapsed, auth.DefaultAttemptFloor)
	}
}

// TestTimebox_NoFloorSkipsSleep covers the floor<=0 branch as the
// behavioural contract for tests that disable the floor.
func TestTimebox_NoFloorSkipsSleep(t *testing.T) {
	start := time.Now()
	called := false
	auth.Timebox(0, func() { called = true })
	if !called {
		t.Fatal("Timebox(0, inner) did not invoke inner")
	}
	if time.Since(start) > 5*time.Millisecond {
		t.Fatalf("Timebox(0) slept %v; expected near-zero", time.Since(start))
	}
}

// Ctx-suffixed shims for auth.UserProvider, added in Sweep 1b.
func (p *timingTestProvider) FindByIDCtx(_ context.Context, id interface{}) (auth.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *timingTestProvider) FindByCredentialsCtx(_ context.Context, credentials map[string]interface{}) (auth.Authenticatable, error) {
	return p.FindByCredentials(credentials)
}
func (p *timingTestProvider) UpdateRememberTokenCtx(_ context.Context, user auth.Authenticatable, token string) error {
	return p.UpdateRememberToken(user, token)
}
