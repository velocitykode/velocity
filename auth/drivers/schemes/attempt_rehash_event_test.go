package schemes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/events"
)

// rehashStubHasher reports NeedsRehash according to the configured flag
// while otherwise behaving like a permissive hasher (Hash and Verify
// trivially succeed). Used so the test does not have to pay real bcrypt
// for a cost-bump scenario.
type rehashStubHasher struct {
	needs bool
}

func (h *rehashStubHasher) Hash(password string) (string, error) { return "stub:" + password, nil }
func (h *rehashStubHasher) Verify(password, hash string) bool    { return hash == "stub:"+password }
func (h *rehashStubHasher) NeedsRehash(string) bool              { return h.needs }

// rehashStubStore validates credentials using the stub hasher.
type rehashStubStore struct {
	user   *timingTestUser
	hasher auth.Hasher
}

func (p *rehashStubStore) FindByID(id interface{}) (contract.Authenticatable, error) {
	return p.user, nil
}
func (p *rehashStubStore) FindByCredentials(creds map[string]interface{}) (contract.Authenticatable, error) {
	return p.user, nil
}
func (p *rehashStubStore) ValidateCredentials(_ contract.Authenticatable, creds map[string]interface{}) bool {
	pw, _ := creds["password"].(string)
	return p.hasher.Verify(pw, p.user.password)
}
func (p *rehashStubStore) UpdateRememberToken(contract.Authenticatable, string) error { return nil }

func newRehashScheme(t *testing.T, needsRehash bool) (*SessionScheme, *rehashStubHasher) {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	stub := &rehashStubHasher{needs: needsRehash}
	user := &timingTestUser{id: "alice@example.com", password: "stub:correct"}
	scheme, err := NewSessionScheme(&rehashStubStore{user: user, hasher: stub}, auth.SessionConfig{
		Name:         "vel_session",
		IdleLifetime: 60,
		Path:         "/",
		HttpOnly:     true,
		SameSite:     http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}
	scheme.SetHasher(stub)
	scheme.SetAttemptFloor(-1)
	return scheme, stub
}

// TestSessionScheme_Attempt_EmitsRehashEvent verifies M-08: after a
// successful login against a hash that no longer matches the configured
// Hasher parameters, the scheme dispatches auth.PasswordNeedsRehashEvent
// with the user identifier. The plaintext is NOT included.
func TestSessionScheme_Attempt_EmitsRehashEvent(t *testing.T) {
	scheme, _ := newRehashScheme(t, true)

	var events []auth.PasswordNeedsRehashEvent
	var mu sync.Mutex
	scheme.SetEventDispatcher(func(_ context.Context, event any) error {
		mu.Lock()
		defer mu.Unlock()
		if ev, ok := event.(auth.PasswordNeedsRehashEvent); ok {
			events = append(events, ev)
		}
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	ok, err := scheme.Attempt(w, r, map[string]interface{}{
		"email":    "alice@example.com",
		"password": "correct",
	})
	if err != nil {
		t.Fatalf("Attempt err = %v", err)
	}
	if !ok {
		t.Fatal("Attempt returned !ok for a valid credential pair")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("expected 1 PasswordNeedsRehashEvent, got %d", len(events))
	}
	got := events[0]
	if got.UserID != "alice@example.com" {
		t.Errorf("event UserID = %v, want alice@example.com", got.UserID)
	}
	if got.SchemeName != "session" {
		t.Errorf("event SchemeName = %q, want session", got.SchemeName)
	}
	if got.Name() != "auth.password.rehash.needed" {
		t.Errorf("Name = %q, want auth.password.rehash.needed", got.Name())
	}
}

// TestSessionScheme_Attempt_NoEventWhenHashFresh confirms the rehash event
// fires only when NeedsRehash returns true.
func TestSessionScheme_Attempt_NoEventWhenHashFresh(t *testing.T) {
	scheme, _ := newRehashScheme(t, false)

	var fired bool
	scheme.SetEventDispatcher(func(context.Context, any) error {
		fired = true
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	if _, err := scheme.Attempt(w, r, map[string]interface{}{
		"email":    "alice@example.com",
		"password": "correct",
	}); err != nil {
		t.Fatalf("Attempt err = %v", err)
	}
	if fired {
		t.Error("PasswordNeedsRehashEvent fired for a fresh hash; want no emission")
	}
}

// TestSessionScheme_Attempt_NoEventOnInvalidPassword confirms the rehash
// event is gated by login success; a failed Attempt must not surface the
// signal.
func TestSessionScheme_Attempt_NoEventOnInvalidPassword(t *testing.T) {
	scheme, _ := newRehashScheme(t, true)

	var fired bool
	scheme.SetEventDispatcher(func(context.Context, any) error {
		fired = true
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	if _, err := scheme.Attempt(w, r, map[string]interface{}{
		"email":    "alice@example.com",
		"password": "wrong",
	}); err != nil {
		t.Fatalf("Attempt err = %v", err)
	}
	if fired {
		t.Error("PasswordNeedsRehashEvent fired on invalid password; want no emission")
	}
}

// TestManager_SetEventDispatcher_PropagatesToSchemes verifies the
// EventDispatcherReceiver propagation pattern: a dispatcher installed on
// the Manager reaches every registered scheme implementing the receiver.
func TestManager_SetEventDispatcher_PropagatesToSchemes(t *testing.T) {
	mgr := auth.NewManager()
	scheme, _ := newRehashScheme(t, true)
	mgr.RegisterScheme("web", scheme)

	var seen bool
	mgr.SetEventDispatcher(func(context.Context, any) error {
		seen = true
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	if _, err := scheme.Attempt(w, r, map[string]interface{}{
		"email":    "alice@example.com",
		"password": "correct",
	}); err != nil {
		t.Fatalf("Attempt err = %v", err)
	}
	if !seen {
		t.Error("Manager.SetEventDispatcher must propagate to registered schemes")
	}
}

// Ctx-suffixed shims for auth.UserStore, added in Sweep 1b.
func (p *rehashStubStore) FindByIDCtx(_ context.Context, id interface{}) (contract.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *rehashStubStore) FindByCredentialsCtx(_ context.Context, credentials map[string]interface{}) (contract.Authenticatable, error) {
	return p.FindByCredentials(credentials)
}
func (p *rehashStubStore) UpdateRememberTokenCtx(_ context.Context, user contract.Authenticatable, token string) error {
	return p.UpdateRememberToken(user, token)
}

// rehashNameListener counts the events it receives.
type rehashNameListener struct {
	mu     sync.Mutex
	events []any
}

func (l *rehashNameListener) Handle(_ context.Context, event any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
	return nil
}

func (l *rehashNameListener) Async() bool { return false }

func (l *rehashNameListener) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.events)
}

// attemptStaleLogin runs one successful Attempt against a stale hash with
// dispatch wired as the scheme's event dispatcher.
func attemptStaleLogin(t *testing.T, dispatch func(context.Context, any) error) {
	t.Helper()
	scheme, _ := newRehashScheme(t, true)
	scheme.SetEventDispatcher(dispatch)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	ok, err := scheme.Attempt(w, r, map[string]interface{}{
		"email":    "alice@example.com",
		"password": "correct",
	})
	if err != nil || !ok {
		t.Fatalf("Attempt = (%v, %v), want (true, nil)", ok, err)
	}
}

// TestSessionScheme_Attempt_RehashEventReachesListenerByName verifies that
// a listener registered on the framework dispatcher under the event's name
// fires on a successful login that needs a rehash, and that the fake
// dispatcher finds the event under that same name.
func TestSessionScheme_Attempt_RehashEventReachesListenerByName(t *testing.T) {
	name := auth.PasswordNeedsRehashEvent{}.Name()
	if name != "auth.password.rehash.needed" {
		t.Fatalf("PasswordNeedsRehashEvent.Name() = %q, want auth.password.rehash.needed", name)
	}

	d := events.NewDispatcher()
	listener := &rehashNameListener{}
	d.Listen(name, listener)
	attemptStaleLogin(t, d.Dispatch)
	if got := listener.count(); got != 1 {
		t.Fatalf("listener under %q fired %d times, want 1", name, got)
	}

	fake := events.NewFakeDispatcher()
	attemptStaleLogin(t, fake.Dispatch)
	if err := fake.AssertDispatched(name, nil); err != nil {
		t.Fatalf("FakeDispatcher.AssertDispatched(%q): %v", name, err)
	}
}
