package schemes

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
)

// admittingThrottler is countingThrottler plus a store-shaped
// contract.LoginAdmitter, standing in for the cache-backed default.
type admittingThrottler struct {
	*countingThrottler
	slotMu sync.Mutex
	slots  map[string]time.Time
}

func newAdmittingThrottler(pair, ident, ip int64, base, max time.Duration) *admittingThrottler {
	return &admittingThrottler{countingThrottler: newCountingThrottler(pair, ident, ip, base, max), slots: map[string]time.Time{}}
}

func (t *admittingThrottler) Admit(_ *http.Request, key string, hold time.Duration) bool {
	t.slotMu.Lock()
	defer t.slotMu.Unlock()
	if until, held := t.slots[key]; held && until.After(time.Now()) {
		return false
	}
	t.slots[key] = time.Now().Add(hold)
	return true
}

func (t *admittingThrottler) RecordSuccess(r *http.Request, key string) {
	t.countingThrottler.RecordSuccess(r, key)
	t.slotMu.Lock()
	delete(t.slots, key)
	t.slotMu.Unlock()
}

// overCapScheme returns a session scheme whose identifier bucket for
// victim@example.test is already at cap (20 failures from 20 addresses),
// with the pair and IP buckets untouched.
func overCapScheme(t *testing.T, throttler interface {
	Allow(*http.Request, string) bool
	RecordFailure(*http.Request, string)
	RecordSuccess(*http.Request, string)
}, store *delayTestStore) *SessionScheme {
	t.Helper()
	scheme := newThrottleDimensionsSessionScheme(t, &mockSessionSchemeUserStore{}, nil)
	scheme.SetUserStore(store)
	scheme.SetLoginThrottler(throttler)
	for i := 0; i < 20; i++ {
		r := loginRequest(fmt.Sprintf("203.0.113.%d:4000", i+1))
		for _, key := range auth.ThrottleKeys(r, map[string]interface{}{"email": "victim@example.test"}, nil) {
			throttler.RecordFailure(r, key)
		}
	}
	return scheme
}

// gatedStore is a delayTestStore whose credential check blocks until
// the test releases it, so a test can prove that every other concurrent
// attempt was turned away while one trial is in flight, independent of
// scheduling order.
func gatedStore(correct string, entered chan<- struct{}, release <-chan struct{}) *delayTestStore {
	s := &delayTestStore{}
	s.validateCredentialsFunc = func(_ auth.Authenticatable, c map[string]interface{}) bool {
		s.mu.Lock()
		s.checks++
		s.mu.Unlock()
		entered <- struct{}{}
		<-release
		return c["password"] == correct
	}
	return s
}

// TestSessionScheme_Attempt_ParallelOverCapTrials_AdmittedOnePerHold is
// the reviewer's concurrency probe, inverted: with the identifier at
// cap, 25 concurrent candidates from 25 source addresses (one of them
// the correct password) must yield exactly one credential check per
// delay window; the rest are denied before verification while that
// trial is in flight, so the correct password cannot slip through the
// crowd. The admitted trial is held inside the user store until every
// other attempt has returned, which makes the assertion independent of
// goroutine scheduling.
func TestSessionScheme_Attempt_ParallelOverCapTrials_AdmittedOnePerHold(t *testing.T) {
	const hold = time.Second
	cases := []struct {
		name      string
		throttler interface {
			Allow(*http.Request, string) bool
			RecordFailure(*http.Request, string)
			RecordSuccess(*http.Request, string)
		}
	}{
		{name: "store-backed admitter on the throttler", throttler: newAdmittingThrottler(5, 20, 50, hold, hold)},
		{name: "per-process fallback admitter", throttler: newCountingThrottler(5, 20, 50, hold, hold)},
		{name: "reserve-before-verify throttler", throttler: newReservingThrottler(5, 20, 50, hold, hold)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entered := make(chan struct{}, 32)
			release := make(chan struct{})
			store := gatedStore("correct", entered, release)
			scheme := overCapScheme(t, tc.throttler, store)

			const parallel = 25
			results := make([]error, parallel)
			oks := make([]bool, parallel)
			finished := make(chan int, parallel)
			var wg sync.WaitGroup
			for i := 0; i < parallel; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					password := "wrong"
					if i == parallel-1 {
						password = "correct"
					}
					r := loginRequest(fmt.Sprintf("198.51.100.%d:5000", i+1))
					oks[i], results[i] = scheme.Attempt(httptest.NewRecorder(), r, map[string]interface{}{"email": "victim@example.test", "password": password})
					finished <- i
				}(i)
			}

			// Exactly one trial enters the store; every other attempt
			// must finish (denied) while it is held there.
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("no trial reached the credential check")
			}
			deniedWhileHeld := 0
			timeout := time.After(5 * time.Second)
			for deniedWhileHeld < parallel-1 {
				select {
				case <-finished:
					deniedWhileHeld++
				case <-entered:
					t.Fatal("a second trial reached the credential check while the slot was held")
				case <-timeout:
					t.Fatalf("only %d of %d attempts were denied while the slot was held", deniedWhileHeld, parallel-1)
				}
			}
			close(release)
			wg.Wait()

			throttled, succeeded := 0, 0
			for i := range results {
				switch {
				case oks[i]:
					succeeded++
				case errors.Is(results[i], auth.ErrLoginThrottled):
					throttled++
				default:
					t.Fatalf("attempt %d: = (%v, %v), want throttled or success", i, oks[i], results[i])
				}
			}
			if got := store.verified(); got != 1 {
				t.Fatalf("verified %d candidates concurrently, want exactly 1 (admission slot)", got)
			}
			if succeeded > 1 || throttled != parallel-succeeded {
				t.Fatalf("succeeded %d, throttled %d, want at most 1 and %d", succeeded, throttled, parallel-succeeded)
			}
		})
	}
}

// TestSessionScheme_Attempt_SlotDenial_DoesNotRecordFailure pins that an
// attempt turned away at the admission slot neither reaches the user
// store nor ratchets the failure count, so a flood of denied requests
// cannot grow the delay the account holder will pay.
func TestSessionScheme_Attempt_SlotDenial_DoesNotRecordFailure(t *testing.T) {
	throttler := newAdmittingThrottler(5, 20, 50, time.Second, time.Second)
	store := newDelayTestStore("correct")
	scheme := overCapScheme(t, throttler, store)
	identifierKey := ""
	for _, key := range auth.ThrottleKeys(loginRequest("203.0.113.1:1"), map[string]interface{}{"email": "victim@example.test"}, nil) {
		if strings.HasPrefix(key, auth.ThrottleKeyIdentifierPrefix) {
			identifierKey = key
		}
	}
	if !throttler.Admit(nil, identifierKey, time.Second) {
		t.Fatal("test setup: could not pre-claim slot")
	}
	before := throttler.counts[identifierKey]

	start := time.Now()
	ok, err := scheme.Attempt(httptest.NewRecorder(), loginRequest("198.51.100.7:5000"), map[string]interface{}{"email": "victim@example.test", "password": "wrong"})
	if ok || !errors.Is(err, auth.ErrLoginThrottled) || errors.Is(err, auth.ErrLoginChallengeRequired) {
		t.Fatalf("Attempt = (%v, %v), want (false, ErrLoginThrottled) with no challenge configured", ok, err)
	}
	if time.Since(start) >= time.Second {
		t.Fatal("slot denial paid the delay; want an immediate (floor-padded) denial")
	}
	if store.verified() != 0 {
		t.Fatal("slot denial reached the credential check")
	}
	if got := throttler.counts[identifierKey]; got != before {
		t.Fatalf("slot denial recorded a failure: count %d -> %d", before, got)
	}
}

// TestSessionScheme_Attempt_Challenge covers the configured-challenge
// paths: a held slot yields ErrLoginChallengeRequired, and a request
// that passes the challenge is admitted with neither slot nor delay.
func TestSessionScheme_Attempt_Challenge(t *testing.T) {
	const hold = 500 * time.Millisecond
	newOverCap := func(t *testing.T) (*SessionScheme, *admittingThrottler, *delayTestStore) {
		throttler := newAdmittingThrottler(5, 20, 50, hold, hold)
		store := newDelayTestStore("correct")
		return overCapScheme(t, throttler, store), throttler, store
	}
	challenge := func(r *http.Request) bool { return r.Header.Get("X-Challenge") == "passed" }

	t.Run("held slot reports challenge required", func(t *testing.T) {
		scheme, throttler, store := newOverCap(t)
		scheme.SetLoginChallenge(challenge)
		key := ""
		for _, k := range auth.ThrottleKeys(loginRequest("203.0.113.1:1"), map[string]interface{}{"email": "victim@example.test"}, nil) {
			if strings.HasPrefix(k, auth.ThrottleKeyIdentifierPrefix) {
				key = k
			}
		}
		throttler.Admit(nil, key, hold)
		ok, err := scheme.Attempt(httptest.NewRecorder(), loginRequest("198.51.100.7:5000"), map[string]interface{}{"email": "victim@example.test", "password": "correct"})
		if ok || !errors.Is(err, auth.ErrLoginChallengeRequired) {
			t.Fatalf("Attempt = (%v, %v), want (false, ErrLoginChallengeRequired)", ok, err)
		}
		if store.verified() != 0 {
			t.Fatal("challenge-required denial reached the credential check")
		}
	})

	t.Run("passed challenge skips slot and delay", func(t *testing.T) {
		scheme, throttler, store := newOverCap(t)
		scheme.SetLoginChallenge(challenge)
		key := ""
		for _, k := range auth.ThrottleKeys(loginRequest("203.0.113.1:1"), map[string]interface{}{"email": "victim@example.test"}, nil) {
			if strings.HasPrefix(k, auth.ThrottleKeyIdentifierPrefix) {
				key = k
			}
		}
		throttler.Admit(nil, key, hold) // attacker holds the slot
		r := loginRequest("198.51.100.7:5000")
		r.Header.Set("X-Challenge", "passed")
		start := time.Now()
		ok, err := scheme.Attempt(httptest.NewRecorder(), r, map[string]interface{}{"email": "victim@example.test", "password": "correct"})
		if !ok || err != nil {
			t.Fatalf("Attempt = (%v, %v), want (true, nil)", ok, err)
		}
		if time.Since(start) >= hold {
			t.Fatal("challenged request still paid the delay")
		}
		if store.verified() != 1 {
			t.Fatalf("verified %d, want 1", store.verified())
		}
	})

	t.Run("passed challenge does not bypass the pair cap", func(t *testing.T) {
		throttler := newAdmittingThrottler(5, 20, 50, hold, hold)
		store := newDelayTestStore("correct")
		scheme := newThrottleDimensionsSessionScheme(t, &mockSessionSchemeUserStore{}, nil)
		scheme.SetUserStore(store)
		scheme.SetLoginThrottler(throttler)
		scheme.SetLoginChallenge(challenge)
		r := loginRequest("198.51.100.7:5000")
		for _, k := range auth.ThrottleKeys(r, map[string]interface{}{"email": "victim@example.test"}, nil) {
			for i := 0; i < 5; i++ {
				throttler.RecordFailure(r, k)
			}
		}
		r.Header.Set("X-Challenge", "passed")
		ok, err := scheme.Attempt(httptest.NewRecorder(), r, map[string]interface{}{"email": "victim@example.test", "password": "correct"})
		if ok || !errors.Is(err, auth.ErrLoginThrottled) {
			t.Fatalf("Attempt = (%v, %v), want (false, ErrLoginThrottled): pair cap is hard", ok, err)
		}
		if store.verified() != 0 {
			t.Fatal("hard denial reached the credential check")
		}
	})

	t.Run("wrong password with passed challenge is still a failure", func(t *testing.T) {
		scheme, _, store := newOverCap(t)
		scheme.SetLoginChallenge(challenge)
		r := loginRequest("198.51.100.7:5000")
		r.Header.Set("X-Challenge", "passed")
		ok, err := scheme.Attempt(httptest.NewRecorder(), r, map[string]interface{}{"email": "victim@example.test", "password": "wrong"})
		if ok || !errors.Is(err, auth.ErrLoginThrottled) {
			t.Fatalf("Attempt = (%v, %v), want (false, ErrLoginThrottled)", ok, err)
		}
		if store.verified() != 1 {
			t.Fatalf("verified %d, want 1", store.verified())
		}
	})
}

// TestManager_SetLoginChallenge_Propagates covers propagation to schemes
// registered before and after the call.
func TestManager_SetLoginChallenge_Propagates(t *testing.T) {
	m := auth.NewManager()
	before := newThrottleDimensionsSessionScheme(t, &mockSessionSchemeUserStore{}, &recordingThrottler{})
	m.RegisterScheme("before", before)
	m.SetLoginChallenge(func(*http.Request) bool { return true })
	after := newThrottleDimensionsSessionScheme(t, &mockSessionSchemeUserStore{}, &recordingThrottler{})
	m.RegisterScheme("after", after)
	if before.getLoginChallenge() == nil || after.getLoginChallenge() == nil {
		t.Fatal("SetLoginChallenge did not reach every registered scheme")
	}
	m.SetLoginChallenge(nil)
	if before.getLoginChallenge() != nil || after.getLoginChallenge() != nil {
		t.Fatal("SetLoginChallenge(nil) did not clear the schemes")
	}
}

// TestJWTScheme_Attempt_ParallelOverCapTrials_AdmittedOnePerHold mirrors
// the admission check on the JWT scheme surface with the per-process
// fallback admitter.
func TestJWTScheme_Attempt_ParallelOverCapTrials_AdmittedOnePerHold(t *testing.T) {
	const hold = time.Second
	throttler := newCountingThrottler(5, 20, 50, hold, hold)
	entered := make(chan struct{}, 32)
	release := make(chan struct{})
	store := gatedStore("correct", entered, release)
	scheme, err := NewJWTScheme(store, auth.JWTConfig{Secret: strings.Repeat("s", 64), Algorithm: "HS256", TTL: 60})
	if err != nil {
		t.Fatalf("NewJWTScheme: %v", err)
	}
	scheme.SetAttemptFloor(-1)
	scheme.SetLoginThrottler(throttler)
	for i := 0; i < 20; i++ {
		r := loginRequest(fmt.Sprintf("203.0.113.%d:4000", i+1))
		for _, key := range auth.ThrottleKeys(r, map[string]interface{}{"email": "victim@example.test"}, nil) {
			throttler.RecordFailure(r, key)
		}
	}
	const parallel = 25
	finished := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := loginRequest(fmt.Sprintf("198.51.100.%d:5000", i+1))
			_, _ = scheme.Attempt(httptest.NewRecorder(), r, map[string]interface{}{"email": "victim@example.test", "password": "wrong"})
			finished <- struct{}{}
		}(i)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no trial reached the credential check")
	}
	denied := 0
	timeout := time.After(5 * time.Second)
	for denied < parallel-1 {
		select {
		case <-finished:
			denied++
		case <-entered:
			t.Fatal("a second trial reached the credential check while the slot was held")
		case <-timeout:
			t.Fatalf("only %d of %d attempts were denied while the slot was held", denied, parallel-1)
		}
	}
	close(release)
	wg.Wait()
	if got := store.verified(); got != 1 {
		t.Fatalf("verified %d candidates concurrently, want exactly 1", got)
	}
}
