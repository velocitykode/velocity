package velocity

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
)

func TestCacheLoginThrottler_Admit_OneTrialPerHold(t *testing.T) {
	th := newTestDimensionedLoginThrottler(t, 5, 20, 50)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	const key = auth.ThrottleKeyIdentifierPrefix + "victim"

	if !th.Admit(r, key, 200*time.Millisecond) {
		t.Fatal("first Admit = false, want true")
	}
	if th.Admit(r, key, 200*time.Millisecond) {
		t.Fatal("second Admit inside hold = true, want false")
	}
	if !th.Admit(r, auth.ThrottleKeyIdentifierPrefix+"other", 200*time.Millisecond) {
		t.Fatal("Admit for another key = false, want true")
	}
	th.RecordSuccess(r, key)
	if !th.Admit(r, key, 200*time.Millisecond) {
		t.Fatal("Admit after RecordSuccess = false, want true (slot released)")
	}
	time.Sleep(250 * time.Millisecond)
	if !th.Admit(r, key, 200*time.Millisecond) {
		t.Fatal("Admit after hold expiry = false, want true")
	}
}

func TestCacheLoginThrottler_Admit_ConcurrentAdmitsOne(t *testing.T) {
	th := newTestDimensionedLoginThrottler(t, 5, 20, 50)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	const key = auth.ThrottleKeyIdentifierPrefix + "victim"
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if th.Admit(r, key, time.Minute) {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != 1 {
		t.Fatalf("admitted %d concurrent trials, want exactly 1", admitted)
	}
}

func TestCacheLoginThrottler_Admit_ZeroHoldAndNilSafe(t *testing.T) {
	th := newTestDimensionedLoginThrottler(t, 5, 20, 50)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	if !th.Admit(r, "k", 0) || !th.Admit(r, "k", 0) {
		t.Fatal("hold 0 must admit unconditionally")
	}
	var nilTh *cacheLoginThrottler
	if !nilTh.Admit(r, "k", time.Second) || !(&cacheLoginThrottler{}).Admit(nil, "k", time.Second) {
		t.Fatal("nil / storeless throttler must admit")
	}
}
