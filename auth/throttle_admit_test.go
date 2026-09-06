package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

func TestLocalLoginAdmitter_OneSlotPerKeyPerHold(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	a := &LocalLoginAdmitter{now: func() time.Time { return now }}
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	const key = ThrottleKeyIdentifierPrefix + "victim"

	if !a.Admit(r, key, time.Second) {
		t.Fatal("first Admit = false, want true")
	}
	if a.Admit(r, key, time.Second) {
		t.Fatal("second Admit inside hold = true, want false")
	}
	if !a.Admit(r, ThrottleKeyIdentifierPrefix+"other", time.Second) {
		t.Fatal("Admit for an unrelated key = false, want true (slots are per key)")
	}
	now = now.Add(999 * time.Millisecond)
	if a.Admit(r, key, time.Second) {
		t.Fatal("Admit 1ms before hold expiry = true, want false")
	}
	now = now.Add(time.Millisecond)
	if !a.Admit(r, key, time.Second) {
		t.Fatal("Admit at hold expiry = false, want true")
	}
	a.Release(key)
	if !a.Admit(r, key, time.Second) {
		t.Fatal("Admit after Release = false, want true")
	}
}

func TestLocalLoginAdmitter_ZeroHoldAndNilAdmit(t *testing.T) {
	var a *LocalLoginAdmitter
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	if !a.Admit(r, "k", time.Second) {
		t.Fatal("nil admitter must admit")
	}
	a.Release("k") // must not panic
	zero := &LocalLoginAdmitter{}
	if !zero.Admit(r, "k", 0) || !zero.Admit(r, "k", 0) {
		t.Fatal("hold 0 must admit unconditionally")
	}
	if !zero.Admit(nil, "k", time.Second) {
		t.Fatal("nil request must be tolerated")
	}
}

func TestLocalLoginAdmitter_SweepsExpiredSlots(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	a := &LocalLoginAdmitter{now: func() time.Time { return now }}
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	for i := 0; i < localAdmitterSweepAt; i++ {
		a.Admit(r, ThrottleKeyIdentifierPrefix+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+string(rune('a'+(i/676)%26)), time.Second)
	}
	if got := len(a.slots); got != localAdmitterSweepAt {
		t.Fatalf("slots before sweep = %d, want %d", got, localAdmitterSweepAt)
	}
	now = now.Add(2 * time.Second)
	a.Admit(r, ThrottleKeyIdentifierPrefix+"fresh", time.Second)
	if got := len(a.slots); got != 1 {
		t.Fatalf("slots after sweep = %d, want 1 (expired entries dropped)", got)
	}
}

func TestLocalLoginAdmitter_ConcurrentAdmitsOne(t *testing.T) {
	a := &LocalLoginAdmitter{}
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	const key = ThrottleKeyIdentifierPrefix + "victim"
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a.Admit(r, key, time.Minute) {
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

type admitterThrottler struct {
	NoopLoginThrottler
	calls int
	admit bool
}

func (a *admitterThrottler) Admit(*http.Request, string, time.Duration) bool {
	a.calls++
	return a.admit
}

func TestAdmitIdentifierTrial(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	const key = ThrottleKeyIdentifierPrefix + "victim"

	t.Run("zero hold admits without consulting anyone", func(t *testing.T) {
		th := &admitterThrottler{admit: false}
		if !AdmitIdentifierTrial(th, &LocalLoginAdmitter{}, r, key, 0) || th.calls != 0 {
			t.Fatalf("hold 0: admitted=%v calls=%d, want true/0", !th.admit, th.calls)
		}
	})
	t.Run("throttler LoginAdmitter wins over fallback", func(t *testing.T) {
		th := &admitterThrottler{admit: false}
		fallback := &LocalLoginAdmitter{}
		if AdmitIdentifierTrial(th, fallback, r, key, time.Second) || th.calls != 1 {
			t.Fatalf("throttler denial not honoured (calls=%d)", th.calls)
		}
		if !fallback.Admit(r, key, time.Second) {
			t.Fatal("fallback must be untouched when the throttler admits")
		}
	})
	t.Run("throttler without capability uses fallback", func(t *testing.T) {
		var th contract.LoginThrottler = NoopLoginThrottler{}
		fallback := &LocalLoginAdmitter{}
		if !AdmitIdentifierTrial(th, fallback, r, key, time.Second) {
			t.Fatal("first fallback Admit = false")
		}
		if AdmitIdentifierTrial(th, fallback, r, key, time.Second) {
			t.Fatal("second fallback Admit inside hold = true")
		}
	})
	t.Run("nil fallback without capability admits", func(t *testing.T) {
		if !AdmitIdentifierTrial(NoopLoginThrottler{}, nil, r, key, time.Second) {
			t.Fatal("nil fallback must admit")
		}
	})
}

func TestErrLoginChallengeRequired_WrapsThrottled(t *testing.T) {
	if !errorsIs(ErrLoginChallengeRequired, ErrLoginThrottled) {
		t.Fatal("ErrLoginChallengeRequired must satisfy errors.Is(err, ErrLoginThrottled)")
	}
}

func errorsIs(err, target error) bool { return errors.Is(err, target) }
