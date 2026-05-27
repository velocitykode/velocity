package app

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestServices_ExtensionsConcurrentRegisterAndRead is the regression test for
// the map mutex sweep (rule #3): RegisterExtension, ExtensionAs, and
// RangeExtensions must be safe to call concurrently. Without the extMu
// protection added in services.go, this test fires "concurrent map read and
// map write" under -race.
//
// The harness fans out N writers (each register a unique key) and M readers
// (each ExtensionAs + a RangeExtensions sweep) over a shared *Services.
// The test passes when every operation returns without panicking and every
// successfully-registered key is observable by ExtensionAs at the end.
func TestServices_ExtensionsConcurrentRegisterAndRead(t *testing.T) {
	t.Parallel()
	const writers = 32
	const readersPerWriter = 4

	s := &Services{}

	var wg sync.WaitGroup
	wg.Add(writers + writers*readersPerWriter)

	keys := make([]string, writers)
	for i := range keys {
		keys[i] = "ext-" + itoa(i)
	}

	var writeErrors int64
	for i := 0; i < writers; i++ {
		k := keys[i]
		go func() {
			defer wg.Done()
			if err := RegisterExtension[string](s, k, k+"-value"); err != nil {
				atomic.AddInt64(&writeErrors, 1)
			}
		}()
		for j := 0; j < readersPerWriter; j++ {
			k := k
			go func() {
				defer wg.Done()
				// Reads may or may not see the value depending on
				// scheduler order; the test is satisfied if no race
				// fires under the read path.
				_, _ = ExtensionAs[string](s, k)
				s.RangeExtensions(func(_ string, _ any) bool {
					return true
				})
			}()
		}
	}

	wg.Wait()

	if writeErrors != 0 {
		t.Fatalf("RegisterExtension reported %d errors; expected 0 (unique keys)", writeErrors)
	}

	for _, k := range keys {
		v, err := ExtensionAs[string](s, k)
		if err != nil {
			t.Fatalf("ExtensionAs(%q) post-fanout: %v", k, err)
		}
		if v != k+"-value" {
			t.Fatalf("ExtensionAs(%q) = %q, want %q", k, v, k+"-value")
		}
	}
}

// TestServices_ExtensionsRegisterRejectsDuplicate pins the duplicate-key
// rejection invariant that RegisterExtension promises in its docstring. The
// mutex change must NOT relax that check (a second registration under the
// same key reports an error).
func TestServices_ExtensionsRegisterRejectsDuplicate(t *testing.T) {
	t.Parallel()
	s := &Services{}
	if err := RegisterExtension[string](s, "k", "v1"); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := RegisterExtension[string](s, "k", "v2"); err == nil {
		t.Fatal("second register: want error, got nil")
	}
	got, err := ExtensionAs[string](s, "k")
	if err != nil {
		t.Fatalf("ExtensionAs after duplicate: %v", err)
	}
	if got != "v1" {
		t.Fatalf("ExtensionAs after duplicate: got %q, want %q (first registration wins)", got, "v1")
	}
}

// TestServices_RangeExtensionsEarlyStop verifies the documented contract that
// fn returning false halts iteration. We rely on iteration order being
// non-deterministic; the test only checks that fn is invoked at most once
// when it returns false on the first call. Two-key minimum to make the
// "saw exactly one" assertion meaningful.
func TestServices_RangeExtensionsEarlyStop(t *testing.T) {
	t.Parallel()
	s := &Services{}
	for _, k := range []string{"a", "b", "c"} {
		if err := RegisterExtension[string](s, k, k); err != nil {
			t.Fatalf("register %q: %v", k, err)
		}
	}
	seen := 0
	s.RangeExtensions(func(_ string, _ any) bool {
		seen++
		return false
	})
	if seen != 1 {
		t.Fatalf("RangeExtensions early stop: visited %d, want 1", seen)
	}
}

// TestServices_RangeExtensionsDoesNotHoldLockAcrossFn is the F1 regression
// test for the Tier 4 re-audit finding: RangeExtensions must NOT hold extMu
// across the user-supplied fn, because (a) a slow fn would block
// RegisterExtension writers, and (b) fn calling back into RegisterExtension
// would deadlock on re-entrant Lock against the held RLock.
//
// The harness pre-registers one extension, then inside fn (a) acquires a
// write lock via RegisterExtension on a fresh key and (b) reads the map
// via ExtensionAs. Both must succeed without deadlocking. Test runs under
// a 2-second timeout via a watchdog goroutine.
func TestServices_RangeExtensionsDoesNotHoldLockAcrossFn(t *testing.T) {
	t.Parallel()
	s := &Services{}
	if err := RegisterExtension[string](s, "seed", "v"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	done := make(chan struct{})
	go func() {
		s.RangeExtensions(func(_ string, _ any) bool {
			if err := RegisterExtension[string](s, "from-fn", "x"); err != nil {
				t.Errorf("register inside fn: %v", err)
			}
			if _, err := ExtensionAs[string](s, "seed"); err != nil {
				t.Errorf("read inside fn: %v", err)
			}
			return true
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RangeExtensions held lock across fn; deadlock on re-entrant Register")
	}
	if _, err := ExtensionAs[string](s, "from-fn"); err != nil {
		t.Fatalf("post-Range registration not visible: %v", err)
	}
}

// itoa is a tiny inline int-to-string helper to avoid pulling strconv into
// the test (keeps the dependency footprint of the app package's first test
// minimal).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
