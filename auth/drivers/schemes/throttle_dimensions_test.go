package schemes

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/crypto"
)

// recordingThrottler captures every key the scheme consults so tests can
// assert the V2-04 multi-dimension fan-out: Allow checked per dimension,
// RecordFailure/RecordSuccess applied to every dimension key.
type recordingThrottler struct {
	mu         sync.Mutex
	denyPrefix string        // deny any key with this prefix; "" allows all
	delay      time.Duration // Delay answer for every key (contract.LoginDelayer)
	allowCalls []string
	failures   []string
	successes  []string
}

// Delay implements contract.LoginDelayer so the scheme pays the fake's
// configured delay (zero by default) instead of the 1s fallback applied
// to throttlers without the capability.
func (t *recordingThrottler) Delay(_ *http.Request, _ string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.delay
}

func (t *recordingThrottler) Allow(_ *http.Request, key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.allowCalls = append(t.allowCalls, key)
	return t.denyPrefix == "" || !strings.HasPrefix(key, t.denyPrefix)
}

func (t *recordingThrottler) RecordFailure(_ *http.Request, key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failures = append(t.failures, key)
}

func (t *recordingThrottler) RecordSuccess(_ *http.Request, key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.successes = append(t.successes, key)
}

func newThrottleDimensionsSessionScheme(t *testing.T, userStore *mockSessionSchemeUserStore, throttler *recordingThrottler) *SessionScheme {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	scheme, err := NewSessionScheme(userStore, auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 60,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}
	scheme.SetAttemptFloor(-1) // bypass the 200ms timebox
	scheme.SetLoginThrottler(throttler)
	return scheme
}

func sortedCopy(keys []string) []string {
	out := append([]string(nil), keys...)
	sort.Strings(out)
	return out
}

func assertSameKeySet(t *testing.T, got, want []string, what string) {
	t.Helper()
	g, w := sortedCopy(got), sortedCopy(want)
	if len(g) != len(w) {
		t.Fatalf("%s: got %d keys %v, want %d keys %v", what, len(g), g, len(w), w)
	}
	for i := range g {
		if g[i] != w[i] {
			t.Fatalf("%s: got %v, want %v", what, g, w)
		}
	}
}

// TestSessionScheme_Attempt_DeniedWhenHardDimensionThrottled proves an
// over-cap per-IP or pair dimension blocks the attempt before the
// credential check (the mock user store would accept the credentials), and
// that the error is the shared auth.ErrLoginThrottled.
func TestSessionScheme_Attempt_DeniedWhenHardDimensionThrottled(t *testing.T) {
	for _, denyPrefix := range []string{
		auth.ThrottleKeyIPPrefix,
		auth.ThrottleKeyPairPrefix,
	} {
		throttler := &recordingThrottler{denyPrefix: denyPrefix}
		scheme := newThrottleDimensionsSessionScheme(t, &mockSessionSchemeUserStore{}, throttler)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		ok, err := scheme.Attempt(w, r, map[string]interface{}{
			"email":    "victim@example.com",
			"password": "x",
		})
		if ok || err != auth.ErrLoginThrottled {
			t.Fatalf("denyPrefix %q: Attempt = (%v, %v), want (false, ErrLoginThrottled)", denyPrefix, ok, err)
		}
		if len(throttler.failures) != 0 {
			t.Fatalf("denyPrefix %q: throttled attempt recorded failures %v, want none", denyPrefix, throttler.failures)
		}
	}
}

// TestSessionScheme_Attempt_IdentifierThrottleAllowsValidCredentials proves
// the identifier dimension is verify-first: an over-cap identifier bucket
// (the distributed-spray shape) cannot lock the account holder out when
// they present the correct password, and the successful login clears every
// dimension bucket.
func TestSessionScheme_Attempt_IdentifierThrottleAllowsValidCredentials(t *testing.T) {
	throttler := &recordingThrottler{denyPrefix: auth.ThrottleKeyIdentifierPrefix}
	scheme := newThrottleDimensionsSessionScheme(t, &mockSessionSchemeUserStore{}, throttler)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	creds := map[string]interface{}{"email": "victim@example.com", "password": "correct"}
	ok, err := scheme.Attempt(w, r, creds)
	if !ok || err != nil {
		t.Fatalf("Attempt = (%v, %v), want (true, nil): over-cap identifier bucket must not lock out valid credentials", ok, err)
	}
	if len(throttler.failures) != 0 {
		t.Fatalf("successful attempt recorded failures %v, want none", throttler.failures)
	}
	assertSameKeySet(t, throttler.successes, auth.ThrottleKeys(r, creds, nil), "RecordSuccess keys")
}

// TestSessionScheme_Attempt_IdentifierThrottleDeniesInvalidCredentials
// proves an over-cap identifier bucket still denies wrong-credential
// attempts, with the shared throttle error and failures recorded on every
// dimension.
func TestSessionScheme_Attempt_IdentifierThrottleDeniesInvalidCredentials(t *testing.T) {
	userStore := &mockSessionSchemeUserStore{
		validateCredentialsFunc: func(auth.Authenticatable, map[string]interface{}) bool { return false },
	}
	throttler := &recordingThrottler{denyPrefix: auth.ThrottleKeyIdentifierPrefix}
	scheme := newThrottleDimensionsSessionScheme(t, userStore, throttler)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	creds := map[string]interface{}{"email": "victim@example.com", "password": "wrong"}
	ok, err := scheme.Attempt(w, r, creds)
	if ok || err != auth.ErrLoginThrottled {
		t.Fatalf("Attempt = (%v, %v), want (false, ErrLoginThrottled)", ok, err)
	}
	assertSameKeySet(t, throttler.failures, auth.ThrottleKeys(r, creds, nil), "RecordFailure keys")
}

// TestSessionScheme_Attempt_RecordsFailureOnEveryDimension proves a failed
// credential check increments all three dimension buckets.
func TestSessionScheme_Attempt_RecordsFailureOnEveryDimension(t *testing.T) {
	userStore := &mockSessionSchemeUserStore{
		findByCredentialsFunc: func(map[string]interface{}) (auth.Authenticatable, error) {
			return nil, nil // user not found
		},
	}
	throttler := &recordingThrottler{}
	scheme := newThrottleDimensionsSessionScheme(t, userStore, throttler)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	creds := map[string]interface{}{"email": "ghost@example.com", "password": "x"}
	if ok, err := scheme.Attempt(w, r, creds); ok || err != nil {
		t.Fatalf("Attempt = (%v, %v), want (false, nil)", ok, err)
	}

	want := auth.ThrottleKeys(r, creds, nil)
	if len(want) != 3 {
		t.Fatalf("ThrottleKeys returned %d keys, want 3", len(want))
	}
	assertSameKeySet(t, throttler.failures, want, "RecordFailure keys")
	assertSameKeySet(t, throttler.allowCalls, want, "Allow keys")
}

// TestJWTScheme_Attempt_DeniedWhenAnyDimensionThrottled mirrors the
// per-dimension denial check on the JWT scheme surface.
func TestJWTScheme_Attempt_DeniedWhenAnyDimensionThrottled(t *testing.T) {
	scheme, err := NewJWTScheme(&mockSessionSchemeUserStore{}, auth.JWTConfig{
		Secret:    strings.Repeat("s", 64),
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTScheme: %v", err)
	}
	scheme.SetAttemptFloor(-1)
	throttler := &recordingThrottler{denyPrefix: auth.ThrottleKeyIPPrefix}
	scheme.SetLoginThrottler(throttler)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	ok, err := scheme.Attempt(w, r, map[string]interface{}{
		"email":    "victim@example.com",
		"password": "x",
	})
	if ok || err != auth.ErrLoginThrottled {
		t.Fatalf("Attempt = (%v, %v), want (false, ErrLoginThrottled)", ok, err)
	}
	if len(throttler.failures) != 0 {
		t.Fatalf("throttled attempt recorded failures %v, want none", throttler.failures)
	}
}

// TestJWTScheme_Attempt_IdentifierThrottleVerifyFirst mirrors the
// verify-first identifier-dimension checks on the JWT scheme surface:
// over-cap identifier bucket allows valid credentials and denies invalid
// ones with the shared throttle error.
func TestJWTScheme_Attempt_IdentifierThrottleVerifyFirst(t *testing.T) {
	newScheme := func(userStore *mockSessionSchemeUserStore, throttler *recordingThrottler) *JWTScheme {
		scheme, err := NewJWTScheme(userStore, auth.JWTConfig{
			Secret:    strings.Repeat("s", 64),
			Algorithm: "HS256",
			TTL:       60,
		})
		if err != nil {
			t.Fatalf("NewJWTScheme: %v", err)
		}
		scheme.SetAttemptFloor(-1)
		scheme.SetLoginThrottler(throttler)
		return scheme
	}

	t.Run("valid credentials allowed", func(t *testing.T) {
		throttler := &recordingThrottler{denyPrefix: auth.ThrottleKeyIdentifierPrefix}
		scheme := newScheme(&mockSessionSchemeUserStore{}, throttler)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		creds := map[string]interface{}{"email": "victim@example.com", "password": "correct"}
		ok, err := scheme.Attempt(w, r, creds)
		if !ok || err != nil {
			t.Fatalf("Attempt = (%v, %v), want (true, nil)", ok, err)
		}
		assertSameKeySet(t, throttler.successes, auth.ThrottleKeys(r, creds, nil), "RecordSuccess keys")
	})

	t.Run("invalid credentials denied", func(t *testing.T) {
		userStore := &mockSessionSchemeUserStore{
			validateCredentialsFunc: func(auth.Authenticatable, map[string]interface{}) bool { return false },
		}
		throttler := &recordingThrottler{denyPrefix: auth.ThrottleKeyIdentifierPrefix}
		scheme := newScheme(userStore, throttler)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		creds := map[string]interface{}{"email": "victim@example.com", "password": "wrong"}
		ok, err := scheme.Attempt(w, r, creds)
		if ok || err != auth.ErrLoginThrottled {
			t.Fatalf("Attempt = (%v, %v), want (false, ErrLoginThrottled)", ok, err)
		}
		assertSameKeySet(t, throttler.failures, auth.ThrottleKeys(r, creds, nil), "RecordFailure keys")
	})
}

// TestJWTScheme_Attempt_RecordsFailureOnEveryDimension mirrors the
// failure fan-out check on the JWT scheme surface.
func TestJWTScheme_Attempt_RecordsFailureOnEveryDimension(t *testing.T) {
	userStore := &mockSessionSchemeUserStore{
		findByCredentialsFunc: func(map[string]interface{}) (auth.Authenticatable, error) {
			return nil, nil
		},
	}
	scheme, err := NewJWTScheme(userStore, auth.JWTConfig{
		Secret:    strings.Repeat("s", 64),
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTScheme: %v", err)
	}
	scheme.SetAttemptFloor(-1)
	throttler := &recordingThrottler{}
	scheme.SetLoginThrottler(throttler)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	creds := map[string]interface{}{"email": "ghost@example.com", "password": "x"}
	if ok, err := scheme.Attempt(w, r, creds); ok || err != nil {
		t.Fatalf("Attempt = (%v, %v), want (false, nil)", ok, err)
	}
	assertSameKeySet(t, throttler.failures, auth.ThrottleKeys(r, creds, nil), "RecordFailure keys")
}
