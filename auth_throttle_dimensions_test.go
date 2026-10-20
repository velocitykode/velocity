package velocity

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
)

// failedAttempt drives the throttler exactly the way the schemes do for a
// failed login (see SessionScheme.Attempt): derive every dimension key,
// check Allow on each, and on an allowed attempt record the failure
// against each. Returns false when any dimension denied the attempt.
func failedAttempt(th *cacheLoginThrottler, remoteAddr, email string) bool {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = remoteAddr
	keys := auth.ThrottleKeys(r, map[string]interface{}{"email": email, "password": "x"}, nil)
	for _, k := range keys {
		if !th.Allow(r, k) {
			return false
		}
	}
	for _, k := range keys {
		th.RecordFailure(r, k)
	}
	return true
}

// TestCacheLoginThrottler_PasswordSpray_TripsPerIPLimit: one IP rotating
// identifiers keeps every pair and identifier bucket at one failure, so
// only the per-IP dimension can stop the spray.
func TestCacheLoginThrottler_PasswordSpray_TripsPerIPLimit(t *testing.T) {
	const perIP = 8
	th := newTestDimensionedLoginThrottler(t, 5, 20, perIP)

	for i := 0; i < perIP; i++ {
		if !failedAttempt(th, "203.0.113.7:40000", fmt.Sprintf("user%d@example.com", i)) {
			t.Fatalf("spray attempt %d denied before per-IP cap of %d", i+1, perIP)
		}
	}
	if failedAttempt(th, "203.0.113.7:40000", "another@example.com") {
		t.Fatalf("spray attempt %d allowed after per-IP cap of %d failures", perIP+1, perIP)
	}
	// A different IP is unaffected (the identifier/pair buckets for this
	// fresh identifier are clean).
	if !failedAttempt(th, "198.51.100.1:40000", "fresh@example.com") {
		t.Fatal("unrelated IP throttled by another IP's spray")
	}
}

// TestCacheLoginThrottler_DistributedBruteForce_TripsPerIdentifierLimit:
// one account attacked from rotating IPs keeps every pair and IP bucket
// at one failure, so only the per-identifier dimension can stop it.
func TestCacheLoginThrottler_DistributedBruteForce_TripsPerIdentifierLimit(t *testing.T) {
	const perIdentifier = 6
	th := newTestDimensionedLoginThrottler(t, 5, perIdentifier, 50)

	for i := 0; i < perIdentifier; i++ {
		if !failedAttempt(th, fmt.Sprintf("10.0.0.%d:1234", i+1), "victim@example.com") {
			t.Fatalf("distributed attempt %d denied before per-identifier cap of %d", i+1, perIdentifier)
		}
	}
	if failedAttempt(th, "10.0.0.99:1234", "victim@example.com") {
		t.Fatalf("distributed attempt %d allowed after per-identifier cap of %d failures", perIdentifier+1, perIdentifier)
	}
	// A different account from a fresh IP is unaffected.
	if !failedAttempt(th, "10.0.0.100:1234", "other@example.com") {
		t.Fatal("unrelated identifier throttled by another account's lockout")
	}
}

// TestCacheLoginThrottler_PairLimitUnchangedForSingleUser: the original
// (identifier, IP) behaviour is preserved; with looser identifier/IP
// caps the pair cap still trips first for a single attacker hammering
// one account.
func TestCacheLoginThrottler_PairLimitUnchangedForSingleUser(t *testing.T) {
	const pair = 3
	th := newTestDimensionedLoginThrottler(t, pair, 20, 50)

	for i := 0; i < pair; i++ {
		if !failedAttempt(th, "203.0.113.7:40000", "victim@example.com") {
			t.Fatalf("attempt %d denied before pair cap of %d", i+1, pair)
		}
	}
	if failedAttempt(th, "203.0.113.7:40000", "victim@example.com") {
		t.Fatalf("attempt %d allowed after pair cap of %d failures", pair+1, pair)
	}
}

// TestCacheLoginThrottler_SuccessClearsAllDimensions: a successful login
// must reset the pair, identifier, and IP buckets. All three caps are
// set equal so a single stale dimension would trip the post-success
// denial one attempt early.
func TestCacheLoginThrottler_SuccessClearsAllDimensions(t *testing.T) {
	th := newTestDimensionedLoginThrottler(t, 2, 2, 2)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = "203.0.113.7:40000"
	keys := auth.ThrottleKeys(r, map[string]interface{}{"email": "victim@example.com", "password": "x"}, nil)

	if !failedAttempt(th, "203.0.113.7:40000", "victim@example.com") {
		t.Fatal("first failure denied")
	}
	for _, k := range keys {
		th.RecordSuccess(r, k)
	}
	for i := 0; i < 2; i++ {
		if !failedAttempt(th, "203.0.113.7:40000", "victim@example.com") {
			t.Fatalf("attempt %d after success denied; a dimension was not cleared", i+1)
		}
	}
	if failedAttempt(th, "203.0.113.7:40000", "victim@example.com") {
		t.Fatal("attempt past the caps allowed; counters not incrementing after reset")
	}
}

// TestCacheLoginThrottler_FixedWindowExpiry: the decay window is set when
// the first failure creates the bucket (cache Add only sets the TTL on a
// missing key) and the bucket vanishes when it lapses.
func TestCacheLoginThrottler_FixedWindowExpiry(t *testing.T) {
	store, err := newMemoryCacheManager().DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore: %v", err)
	}
	th := newCacheLoginThrottler(store, 2, 2, 2, 100*time.Millisecond)

	for i := 0; i < 2; i++ {
		if !failedAttempt(th, "203.0.113.7:40000", "victim@example.com") {
			t.Fatalf("attempt %d denied before cap", i+1)
		}
	}
	if failedAttempt(th, "203.0.113.7:40000", "victim@example.com") {
		t.Fatal("attempt past cap allowed")
	}

	time.Sleep(150 * time.Millisecond)
	if !failedAttempt(th, "203.0.113.7:40000", "victim@example.com") {
		t.Fatal("attempt denied after the fixed window lapsed")
	}
}

func TestConfigFromEnv_PerDimensionLimits(t *testing.T) {
	t.Setenv("AUTH_LOGIN_MAX_ATTEMPTS_PER_IDENTIFIER", "7")
	t.Setenv("AUTH_LOGIN_MAX_ATTEMPTS_PER_IP", "11")
	lt := ConfigFromEnv().Auth.LoginThrottle
	if lt.MaxAttemptsPerIdentifier != 7 {
		t.Fatalf("MaxAttemptsPerIdentifier = %d, want 7", lt.MaxAttemptsPerIdentifier)
	}
	if lt.MaxAttemptsPerIP != 11 {
		t.Fatalf("MaxAttemptsPerIP = %d, want 11", lt.MaxAttemptsPerIP)
	}

	// Invalid and unset values are zero, which the throttler reads as
	// its defaults.
	t.Setenv("AUTH_LOGIN_MAX_ATTEMPTS_PER_IDENTIFIER", "-3")
	t.Setenv("AUTH_LOGIN_MAX_ATTEMPTS_PER_IP", "")
	lt = ConfigFromEnv().Auth.LoginThrottle
	if lt.MaxAttemptsPerIdentifier != 0 || lt.MaxAttemptsPerIP != 0 {
		t.Fatalf("invalid/unset = %d/%d, want 0/0", lt.MaxAttemptsPerIdentifier, lt.MaxAttemptsPerIP)
	}
	th := newCacheLoginThrottler(nil, lt.MaxAttempts, lt.MaxAttemptsPerIdentifier, lt.MaxAttemptsPerIP, lt.Decay)
	if th.identifierMaxAttempts != defaultLoginThrottleIdentifierMaxAttempts {
		t.Fatalf("identifier fallback = %d, want %d", th.identifierMaxAttempts, defaultLoginThrottleIdentifierMaxAttempts)
	}
	if th.ipMaxAttempts != defaultLoginThrottleIPMaxAttempts {
		t.Fatalf("IP fallback = %d, want %d", th.ipMaxAttempts, defaultLoginThrottleIPMaxAttempts)
	}
}

// TestInstallLoginThrottler_PerDimensionConfigWiring proves the configured
// caps reach the installed throttler: with MaxAttemptsPerIP=2
// a spray (distinct identifiers, one IP) is denied on the third attempt
// even though the pair cap is far away.
func TestInstallLoginThrottler_PerDimensionConfigWiring(t *testing.T) {
	manager := auth.NewManager()
	scheme := &fakeLoginThrottlerScheme{}
	manager.RegisterScheme("web", scheme)
	installLoginThrottler(manager, newMemoryCacheManager(), auth.LoginThrottleConfig{
		MaxAttempts:      5,
		MaxAttemptsPerIP: 2,
		Decay:            60 * time.Second,
	}, nil)

	th, ok := scheme.throttler.(*cacheLoginThrottler)
	if !ok {
		t.Fatalf("installed throttler is %T, want *cacheLoginThrottler", scheme.throttler)
	}
	for i := 0; i < 2; i++ {
		if !failedAttempt(th, "203.0.113.7:40000", fmt.Sprintf("user%d@example.com", i)) {
			t.Fatalf("spray attempt %d denied before configured per-IP cap", i+1)
		}
	}
	if failedAttempt(th, "203.0.113.7:40000", "user99@example.com") {
		t.Fatal("spray attempt allowed past configured per-IP cap of 2")
	}
}
