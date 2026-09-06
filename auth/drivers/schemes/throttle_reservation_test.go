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

// reservingThrottler stands in for the cache-backed default's full
// capability set: atomic reserve-before-verify (contract.LoginReserver),
// progressive delay (contract.LoginDelayer) and the admission slot
// (contract.LoginAdmitter). Reserve counts the attempt itself, so Delay
// treats the count as including the caller's reservation.
type reservingThrottler struct {
	*admittingThrottler
}

func newReservingThrottler(pair, ident, ip int64, base, max time.Duration) *reservingThrottler {
	return &reservingThrottler{admittingThrottler: newAdmittingThrottler(pair, ident, ip, base, max)}
}

func (t *reservingThrottler) Reserve(_ *http.Request, key string) (bool, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counts[key]++
	if t.counts[key] <= t.capFor(key) {
		return true, 0
	}
	return false, auth.ProgressiveDelay(t.counts[key]-t.capFor(key), t.base, t.max)
}

func (t *reservingThrottler) count(key string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counts[key]
}

func identifierKeyFor(email string) string {
	for _, k := range auth.ThrottleKeys(loginRequest("203.0.113.1:1"), map[string]interface{}{"email": email}, nil) {
		if strings.HasPrefix(k, auth.ThrottleKeyIdentifierPrefix) {
			return k
		}
	}
	return ""
}

// TestSessionScheme_Attempt_Reservation_CountsOnce proves the reservation
// path counts each attempt exactly once (Reserve, no RecordFailure) and
// that the budget is unchanged: cap 5 verifies 5 candidates, the sixth
// is denied before the store.
func TestSessionScheme_Attempt_Reservation_CountsOnce(t *testing.T) {
	throttler := newReservingThrottler(5, 20, 50, time.Millisecond, 4*time.Millisecond)
	store := newDelayTestStore("correct")
	scheme := newThrottleDimensionsSessionScheme(t, &mockSessionSchemeUserStore{}, nil)
	scheme.SetUserStore(store)
	scheme.SetLoginThrottler(throttler)

	creds := map[string]interface{}{"email": "victim@example.test", "password": "wrong"}
	for i := 0; i < 8; i++ {
		ok, err := scheme.Attempt(httptest.NewRecorder(), loginRequest("198.51.100.10:1234"), creds)
		if i < 5 {
			if ok || err != nil {
				t.Fatalf("attempt %d: = (%v, %v), want (false, nil)", i+1, ok, err)
			}
			continue
		}
		if ok || !errors.Is(err, auth.ErrLoginThrottled) {
			t.Fatalf("attempt %d: = (%v, %v), want (false, ErrLoginThrottled)", i+1, ok, err)
		}
	}
	if got := store.verified(); got != 5 {
		t.Fatalf("verified %d, want 5", got)
	}
	for _, key := range auth.ThrottleKeys(loginRequest("198.51.100.10:1234"), creds, nil) {
		if got := throttler.count(key); got != 8 {
			t.Fatalf("%s counted %d, want 8 (one reservation per attempt, no double count)", key, got)
		}
	}
	// A correct password from a fresh pair/IP is still over the cap for
	// nothing (identifier count 8 < 20), logs in, and clears every key.
	ok, err := scheme.Attempt(httptest.NewRecorder(), loginRequest("198.51.100.11:1234"), map[string]interface{}{"email": "victim@example.test", "password": "correct"})
	if !ok || err != nil {
		t.Fatalf("correct password from a fresh address: = (%v, %v), want (true, nil)", ok, err)
	}
	if got := throttler.count(identifierKeyFor("victim@example.test")); got != 0 {
		t.Fatalf("identifier count after success = %d, want 0", got)
	}
}

// TestSessionScheme_Attempt_ConcurrentBelowCap_ReservesBeforeVerify is
// the reviewer's below-cap probe, inverted: with 19 of 20 identifier
// attempts already counted, 64 concurrent candidates from 64 source
// addresses (one of them correct) must yield at most two credential
// checks, the one remaining within-cap attempt and the single over-cap
// trial the admission slot lets through, with everything else denied
// before verification while those are in flight.
func TestSessionScheme_Attempt_ConcurrentBelowCap_ReservesBeforeVerify(t *testing.T) {
	const hold = time.Second
	throttler := newReservingThrottler(5, 20, 50, hold, hold)
	entered := make(chan struct{}, 128)
	release := make(chan struct{})
	store := gatedStore("correct", entered, release)
	scheme := newThrottleDimensionsSessionScheme(t, &mockSessionSchemeUserStore{}, nil)
	scheme.SetUserStore(store)
	scheme.SetLoginThrottler(throttler)
	for i := 0; i < 19; i++ {
		r := loginRequest(fmt.Sprintf("203.0.113.%d:4000", i+1))
		for _, key := range auth.ThrottleKeys(r, map[string]interface{}{"email": "victim@example.test"}, nil) {
			throttler.Reserve(r, key)
		}
	}

	const parallel = 64
	oks := make([]bool, parallel)
	errs := make([]error, parallel)
	finished := make(chan struct{}, parallel)
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
			oks[i], errs[i] = scheme.Attempt(httptest.NewRecorder(), r, map[string]interface{}{"email": "victim@example.test", "password": password})
			finished <- struct{}{}
		}(i)
	}

	// Budget: one within-cap attempt plus one slot-admitted over-cap
	// trial may reach the store; every other attempt must return
	// denied while those are held there.
	const budget = 2
	inFlight, denied := 0, 0
	timeout := time.After(5 * time.Second)
	for denied < parallel-budget {
		select {
		case <-entered:
			inFlight++
			if inFlight > budget {
				t.Fatalf("%d trials reached the credential check concurrently, want at most %d", inFlight, budget)
			}
		case <-finished:
			denied++
		case <-timeout:
			t.Fatalf("only %d of %d attempts were denied while the budget was in flight (in flight: %d)", denied, parallel-budget, inFlight)
		}
	}
	close(release)
	wg.Wait()

	if got := store.verified(); got > budget {
		t.Fatalf("verified %d candidates, want at most %d", got, budget)
	}
	succeeded := 0
	for i := range oks {
		if oks[i] {
			succeeded++
		} else if !errors.Is(errs[i], auth.ErrLoginThrottled) && errs[i] != nil {
			t.Fatalf("attempt %d: unexpected error %v", i, errs[i])
		}
	}
	if succeeded > 1 {
		t.Fatalf("%d successes, want at most 1", succeeded)
	}
}
