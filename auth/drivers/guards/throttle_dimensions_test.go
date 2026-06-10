package guards

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/crypto"
)

// recordingThrottler captures every key the guard consults so tests can
// assert the V2-04 multi-dimension fan-out: Allow checked per dimension,
// RecordFailure/RecordSuccess applied to every dimension key.
type recordingThrottler struct {
	mu         sync.Mutex
	denyPrefix string // deny any key with this prefix; "" allows all
	allowCalls []string
	failures   []string
	successes  []string
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

func newThrottleDimensionsSessionGuard(t *testing.T, provider *mockSessionGuardUserProvider, throttler *recordingThrottler) *SessionGuard {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
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
	guard.SetAttemptFloor(-1) // bypass the 200ms timebox
	guard.SetLoginThrottler(throttler)
	return guard
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

// TestSessionGuard_Attempt_DeniedWhenAnyDimensionThrottled proves an
// over-cap per-IP dimension alone (spray shape) blocks the attempt even
// though the pair and identifier buckets are clean, and that the error
// is the shared auth.ErrLoginThrottled regardless of dimension.
func TestSessionGuard_Attempt_DeniedWhenAnyDimensionThrottled(t *testing.T) {
	for _, denyPrefix := range []string{
		auth.ThrottleKeyIdentifierPrefix,
		auth.ThrottleKeyIPPrefix,
	} {
		throttler := &recordingThrottler{denyPrefix: denyPrefix}
		guard := newThrottleDimensionsSessionGuard(t, &mockSessionGuardUserProvider{}, throttler)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		ok, err := guard.Attempt(w, r, map[string]interface{}{
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

// TestSessionGuard_Attempt_RecordsFailureOnEveryDimension proves a failed
// credential check increments all three dimension buckets.
func TestSessionGuard_Attempt_RecordsFailureOnEveryDimension(t *testing.T) {
	provider := &mockSessionGuardUserProvider{
		findByCredentialsFunc: func(map[string]interface{}) (auth.Authenticatable, error) {
			return nil, nil // user not found
		},
	}
	throttler := &recordingThrottler{}
	guard := newThrottleDimensionsSessionGuard(t, provider, throttler)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	creds := map[string]interface{}{"email": "ghost@example.com", "password": "x"}
	if ok, err := guard.Attempt(w, r, creds); ok || err != nil {
		t.Fatalf("Attempt = (%v, %v), want (false, nil)", ok, err)
	}

	want := auth.ThrottleKeys(r, creds, nil)
	if len(want) != 3 {
		t.Fatalf("ThrottleKeys returned %d keys, want 3", len(want))
	}
	assertSameKeySet(t, throttler.failures, want, "RecordFailure keys")
	assertSameKeySet(t, throttler.allowCalls, want, "Allow keys")
}

// TestJWTGuard_Attempt_DeniedWhenAnyDimensionThrottled mirrors the
// per-dimension denial check on the JWT guard surface.
func TestJWTGuard_Attempt_DeniedWhenAnyDimensionThrottled(t *testing.T) {
	guard, err := NewJWTGuard(&mockSessionGuardUserProvider{}, auth.JWTConfig{
		Secret:    strings.Repeat("s", 64),
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTGuard: %v", err)
	}
	guard.SetAttemptFloor(-1)
	throttler := &recordingThrottler{denyPrefix: auth.ThrottleKeyIPPrefix}
	guard.SetLoginThrottler(throttler)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	ok, err := guard.Attempt(w, r, map[string]interface{}{
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

// TestJWTGuard_Attempt_RecordsFailureOnEveryDimension mirrors the
// failure fan-out check on the JWT guard surface.
func TestJWTGuard_Attempt_RecordsFailureOnEveryDimension(t *testing.T) {
	provider := &mockSessionGuardUserProvider{
		findByCredentialsFunc: func(map[string]interface{}) (auth.Authenticatable, error) {
			return nil, nil
		},
	}
	guard, err := NewJWTGuard(provider, auth.JWTConfig{
		Secret:    strings.Repeat("s", 64),
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTGuard: %v", err)
	}
	guard.SetAttemptFloor(-1)
	throttler := &recordingThrottler{}
	guard.SetLoginThrottler(throttler)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	creds := map[string]interface{}{"email": "ghost@example.com", "password": "x"}
	if ok, err := guard.Attempt(w, r, creds); ok || err != nil {
		t.Fatalf("Attempt = (%v, %v), want (false, nil)", ok, err)
	}
	assertSameKeySet(t, throttler.failures, auth.ThrottleKeys(r, creds, nil), "RecordFailure keys")
}
