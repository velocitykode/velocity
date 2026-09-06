package schemes

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
)

// countingThrottler is a deterministic per-dimension counter at the
// production 5/20/50 shape with a progressive contract.LoginDelayer. It
// stands in for the cache-backed default so the tests below can drive the
// full attempt loop the schemes run in production.
type countingThrottler struct {
	mu     sync.Mutex
	counts map[string]int64
	pair   int64
	ident  int64
	ip     int64
	base   time.Duration
	max    time.Duration
}

func newCountingThrottler(pair, ident, ip int64, base, max time.Duration) *countingThrottler {
	return &countingThrottler{counts: map[string]int64{}, pair: pair, ident: ident, ip: ip, base: base, max: max}
}

func (t *countingThrottler) capFor(key string) int64 {
	switch {
	case strings.HasPrefix(key, auth.ThrottleKeyIdentifierPrefix):
		return t.ident
	case strings.HasPrefix(key, auth.ThrottleKeyIPPrefix):
		return t.ip
	default:
		return t.pair
	}
}

func (t *countingThrottler) Allow(_ *http.Request, key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counts[key] < t.capFor(key)
}

func (t *countingThrottler) RecordFailure(_ *http.Request, key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counts[key]++
}

func (t *countingThrottler) RecordSuccess(_ *http.Request, key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.counts, key)
}

func (t *countingThrottler) Delay(_ *http.Request, key string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return auth.ProgressiveDelay(t.counts[key]-t.capFor(key)+1, t.base, t.max)
}

// delayTestStore accepts exactly one password and counts credential
// checks so a test can tell how many candidates reached verification.
type delayTestStore struct {
	mockSessionSchemeUserStore
	mu     sync.Mutex
	checks int
}

func newDelayTestStore(correct string) *delayTestStore {
	s := &delayTestStore{}
	s.validateCredentialsFunc = func(_ auth.Authenticatable, c map[string]interface{}) bool {
		s.mu.Lock()
		s.checks++
		s.mu.Unlock()
		return c["password"] == correct
	}
	return s
}

func (s *delayTestStore) verified() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checks
}

func loginRequest(remoteAddr string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = remoteAddr
	return WithSessionContext(r)
}

// TestSessionScheme_Attempt_IdentifierOverCap_PaysDelay proves an
// over-cap identifier bucket pays the throttler's delay on both the
// right- and wrong-password paths, that the delay is the same for both
// (no correctness oracle), and that the account holder still logs in.
func TestSessionScheme_Attempt_IdentifierOverCap_PaysDelay(t *testing.T) {
	const delay = 40 * time.Millisecond
	cases := []struct {
		name     string
		valid    bool
		wantOK   bool
		wantErr  error
		wantKeys string
	}{
		{name: "wrong password pays delay and is throttled", valid: false, wantOK: false, wantErr: auth.ErrLoginThrottled, wantKeys: "failures"},
		{name: "correct password pays delay and logs in", valid: true, wantOK: true, wantErr: nil, wantKeys: "successes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &mockSessionSchemeUserStore{
				validateCredentialsFunc: func(auth.Authenticatable, map[string]interface{}) bool { return tc.valid },
			}
			throttler := &recordingThrottler{denyPrefix: auth.ThrottleKeyIdentifierPrefix, delay: delay}
			scheme := newThrottleDimensionsSessionScheme(t, store, throttler)

			r := loginRequest("203.0.113.9:4000")
			creds := map[string]interface{}{"email": "victim@example.com", "password": "x"}
			start := time.Now()
			ok, err := scheme.Attempt(httptest.NewRecorder(), r, creds)
			elapsed := time.Since(start)

			if ok != tc.wantOK || err != tc.wantErr {
				t.Fatalf("Attempt = (%v, %v), want (%v, %v)", ok, err, tc.wantOK, tc.wantErr)
			}
			if elapsed < delay {
				t.Fatalf("Attempt took %v, want >= %v (identifier over-cap delay not paid)", elapsed, delay)
			}
			want := auth.ThrottleKeys(r, creds, nil)
			if tc.wantKeys == "failures" {
				assertSameKeySet(t, throttler.failures, want, "RecordFailure keys")
			} else {
				assertSameKeySet(t, throttler.successes, want, "RecordSuccess keys")
			}
		})
	}
}

// TestSessionScheme_Attempt_HardDenial_PaysNoIdentifierDelay pins that
// the delay is specific to the identifier dimension: a pair / IP denial
// short-circuits before the credential check without paying it.
func TestSessionScheme_Attempt_HardDenial_PaysNoIdentifierDelay(t *testing.T) {
	const delay = 200 * time.Millisecond
	for _, denyPrefix := range []string{auth.ThrottleKeyIPPrefix, auth.ThrottleKeyPairPrefix} {
		throttler := &recordingThrottler{denyPrefix: denyPrefix, delay: delay}
		scheme := newThrottleDimensionsSessionScheme(t, &mockSessionSchemeUserStore{}, throttler)

		start := time.Now()
		ok, err := scheme.Attempt(httptest.NewRecorder(), loginRequest("203.0.113.9:4000"), map[string]interface{}{
			"email": "victim@example.com", "password": "x",
		})
		if ok || err != auth.ErrLoginThrottled {
			t.Fatalf("denyPrefix %q: Attempt = (%v, %v), want (false, ErrLoginThrottled)", denyPrefix, ok, err)
		}
		if elapsed := time.Since(start); elapsed >= delay {
			t.Fatalf("denyPrefix %q: hard denial took %v, want < %v (identifier delay must not apply)", denyPrefix, elapsed, delay)
		}
	}
}

// TestSessionScheme_Attempt_ThrottlerWithoutDelayer_PaysDefault covers the
// fallback: a LoginThrottler that does not implement contract.LoginDelayer
// still pays auth.DefaultIdentifierDelay once its identifier bucket is
// over cap. Skipped in -short mode because it sleeps for the real default.
func TestSessionScheme_Attempt_ThrottlerWithoutDelayer_PaysDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 1s wall-clock fallback test in -short mode")
	}
	throttler := &plainIdentifierDenier{}
	scheme := newThrottleDimensionsSessionScheme(t, &mockSessionSchemeUserStore{}, nil)
	scheme.SetLoginThrottler(throttler)

	start := time.Now()
	ok, err := scheme.Attempt(httptest.NewRecorder(), loginRequest("203.0.113.9:4000"), map[string]interface{}{
		"email": "victim@example.com", "password": "x",
	})
	if !ok || err != nil {
		t.Fatalf("Attempt = (%v, %v), want (true, nil)", ok, err)
	}
	if elapsed := time.Since(start); elapsed < auth.DefaultIdentifierDelay {
		t.Fatalf("Attempt took %v, want >= %v (fallback delay not paid)", elapsed, auth.DefaultIdentifierDelay)
	}
}

// plainIdentifierDenier denies the identifier dimension and implements
// nothing beyond contract.LoginThrottler.
type plainIdentifierDenier struct{}

func (plainIdentifierDenier) Allow(_ *http.Request, key string) bool {
	return !strings.HasPrefix(key, auth.ThrottleKeyIdentifierPrefix)
}
func (plainIdentifierDenier) RecordFailure(*http.Request, string) {}
func (plainIdentifierDenier) RecordSuccess(*http.Request, string) {}

// TestSessionScheme_Attempt_SpoofedForwardedHeaders_DoNotRotateBuckets is
// the review's TestForwardedHeadersBypassLoginBudgets with the assertion
// inverted: with no trusted proxies, rotating X-Forwarded-For and
// X-Forwarded-Host from one peer lands every attempt in the same pair
// bucket, so the pair cap stops the sixth candidate before verification.
func TestSessionScheme_Attempt_SpoofedForwardedHeaders_DoNotRotateBuckets(t *testing.T) {
	store := newDelayTestStore("correct")
	throttler := newCountingThrottler(5, 20, 50, time.Millisecond, 4*time.Millisecond)
	scheme := newThrottleDimensionsSessionScheme(t, &mockSessionSchemeUserStore{}, nil)
	scheme.SetUserStore(store)
	scheme.SetLoginThrottler(throttler)

	for i := 0; i < 25; i++ {
		r := loginRequest("198.51.100.10:1234")
		r.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i+1))
		r.Header.Set("X-Forwarded-Host", "attacker.example")
		password := "wrong"
		if i == 24 {
			password = "correct"
		}
		ok, err := scheme.Attempt(httptest.NewRecorder(), r, map[string]interface{}{"email": "victim@example.test", "password": password})
		if i < 5 {
			if ok || err != nil {
				t.Fatalf("attempt %d: = (%v, %v), want (false, nil)", i+1, ok, err)
			}
			continue
		}
		if ok || err != auth.ErrLoginThrottled {
			t.Fatalf("attempt %d: = (%v, %v), want (false, ErrLoginThrottled)", i+1, ok, err)
		}
	}
	if got := store.verified(); got != 5 {
		t.Fatalf("verified %d candidates, want 5 (pair budget); spoofed headers rotated the throttle bucket", got)
	}
}

// TestSessionScheme_Attempt_RotatingSourceIPs_BoundedByIdentifierDelay
// covers genuinely distributed guessing: 25 attempts against one account
// from 25 real source addresses. The pair and IP buckets never trip, so
// every candidate reaches verification, but each attempt past the
// identifier cap of 20 pays the progressive delay, and the correct
// candidate on attempt 25 still logs the account holder in.
func TestSessionScheme_Attempt_RotatingSourceIPs_BoundedByIdentifierDelay(t *testing.T) {
	const base, max = 8 * time.Millisecond, 32 * time.Millisecond
	store := newDelayTestStore("correct")
	throttler := newCountingThrottler(5, 20, 50, base, max)
	scheme := newThrottleDimensionsSessionScheme(t, &mockSessionSchemeUserStore{}, nil)
	scheme.SetUserStore(store)
	scheme.SetLoginThrottler(throttler)

	var paid time.Duration
	for i := 0; i < 25; i++ {
		r := loginRequest(fmt.Sprintf("203.0.113.%d:4000", i+1))
		password := "wrong"
		if i == 24 {
			password = "correct"
		}
		start := time.Now()
		ok, err := scheme.Attempt(httptest.NewRecorder(), r, map[string]interface{}{"email": "victim@example.test", "password": password})
		elapsed := time.Since(start)

		switch {
		case i < 20:
			if ok || err != nil {
				t.Fatalf("attempt %d: = (%v, %v), want (false, nil)", i+1, ok, err)
			}
			if elapsed >= base {
				t.Fatalf("attempt %d under identifier cap took %v, want < %v", i+1, elapsed, base)
			}
		case i < 24:
			if ok || err != auth.ErrLoginThrottled {
				t.Fatalf("attempt %d: = (%v, %v), want (false, ErrLoginThrottled)", i+1, ok, err)
			}
			want := auth.ProgressiveDelay(int64(i-20+1), base, max)
			if elapsed < want {
				t.Fatalf("attempt %d over identifier cap took %v, want >= %v", i+1, elapsed, want)
			}
			paid += want
		default:
			if !ok || err != nil {
				t.Fatalf("attempt %d (correct password): = (%v, %v), want (true, nil)", i+1, ok, err)
			}
			if elapsed < max {
				t.Fatalf("correct candidate past cap took %v, want >= %v (must pay the delay too)", elapsed, max)
			}
		}
	}
	if got := store.verified(); got != 25 {
		t.Fatalf("verified %d candidates, want 25 (identifier bucket is verify-first)", got)
	}
	if paid < base+2*base+4*base+max {
		t.Fatalf("total mandated delay past cap = %v, want progressive sum", paid)
	}
	// The successful login cleared the identifier bucket: the holder's
	// next attempt pays nothing.
	r := loginRequest("203.0.113.99:4000")
	start := time.Now()
	if ok, err := scheme.Attempt(httptest.NewRecorder(), r, map[string]interface{}{"email": "victim@example.test", "password": "correct"}); !ok || err != nil {
		t.Fatalf("post-success Attempt = (%v, %v), want (true, nil)", ok, err)
	}
	if elapsed := time.Since(start); elapsed >= base {
		t.Fatalf("post-success attempt took %v, want < %v (bucket should be cleared)", elapsed, base)
	}
}

// TestJWTScheme_Attempt_IdentifierOverCap_PaysDelay mirrors the delay
// check on the JWT scheme surface.
func TestJWTScheme_Attempt_IdentifierOverCap_PaysDelay(t *testing.T) {
	const delay = 40 * time.Millisecond
	for _, valid := range []bool{false, true} {
		store := &mockSessionSchemeUserStore{
			validateCredentialsFunc: func(auth.Authenticatable, map[string]interface{}) bool { return valid },
		}
		scheme, err := NewJWTScheme(store, auth.JWTConfig{Secret: strings.Repeat("s", 64), Algorithm: "HS256", TTL: 60})
		if err != nil {
			t.Fatalf("NewJWTScheme: %v", err)
		}
		scheme.SetAttemptFloor(-1)
		scheme.SetLoginThrottler(&recordingThrottler{denyPrefix: auth.ThrottleKeyIdentifierPrefix, delay: delay})

		start := time.Now()
		ok, attemptErr := scheme.Attempt(httptest.NewRecorder(), loginRequest("203.0.113.9:4000"), map[string]interface{}{
			"email": "victim@example.com", "password": "x",
		})
		if ok != valid {
			t.Fatalf("valid=%v: Attempt = (%v, %v)", valid, ok, attemptErr)
		}
		if elapsed := time.Since(start); elapsed < delay {
			t.Fatalf("valid=%v: Attempt took %v, want >= %v", valid, elapsed, delay)
		}
	}
}
