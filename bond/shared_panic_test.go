package bond

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestMergeSharedProps_PanicInCallbackDoesNotLeakRLock is the regression test
// for security finding E-03. A SharedPropFunc that panics inside
// mergeSharedProps must NOT leave the Bond's RWMutex permanently RLocked.
// Before the fix, the user callback was invoked inside the locked region
// (b.mu.RLock + manual b.mu.RUnlock), so a panic that bypassed the explicit
// RUnlock wedged the lock and every subsequent writer (Share, ShareFunc,
// ClearShared, SetSharePropsFunc) hung forever.
//
// This test:
//  1. Registers a ShareFunc whose closure panics.
//  2. Calls mergeSharedProps from a goroutine, recovering the panic locally.
//  3. From the main goroutine, calls Share() with a 2-second timeout.
//  4. Asserts the Share() call completes (the lock was released).
//
// The whole sequence repeats 10 times to confirm no goroutine leak.
func TestMergeSharedProps_PanicInCallbackDoesNotLeakRLock(t *testing.T) {
	for iter := 0; iter < 10; iter++ {
		b := setupBond(t)

		b.ShareFunc("boom", func(r *http.Request) (any, error) {
			panic("E-03 regression: simulate user callback panic")
		})

		// Goroutine A: trigger the panicking callback through
		// mergeSharedProps. Recover locally so the test process stays
		// alive; the fix should still release the lock cleanly.
		panicDone := make(chan struct{})
		go func() {
			defer close(panicDone)
			defer func() {
				_ = recover() // expected, ignored
			}()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			_ = b.mergeSharedProps(req, Props{})
		}()

		// Wait for the panicking goroutine to finish (panic recovered
		// or completed). The lock state is now determined.
		select {
		case <-panicDone:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: mergeSharedProps goroutine did not finish in 2s", iter)
		}

		// Goroutine B: call a writer. Before the fix this hung forever
		// because the wedged RLock blocks the writer.
		shareDone := make(chan struct{})
		go func() {
			defer close(shareDone)
			b.Share("k", "v")
		}()

		select {
		case <-shareDone:
			// ok, lock was released
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: Share() hung for 2s after a panicking SharedPropFunc; RLock was leaked", iter)
		}
	}
}

// TestMergeSharedProps_PanicAllowsConcurrentReaders ensures readers can
// continue to use mergeSharedProps after a panicking sibling callback.
// Without the fix, queued writers behind the wedged RLock would also block
// readers indirectly (RWMutex contract forbids new readers when a writer is
// queued).
func TestMergeSharedProps_PanicAllowsConcurrentReaders(t *testing.T) {
	b := setupBond(t)

	b.ShareFunc("boom", func(r *http.Request) (any, error) {
		panic("E-03 regression: panic in dynamic prop")
	})
	b.Share("safe", "value")

	// Trigger the panicking callback once to potentially wedge the lock.
	func() {
		defer func() { _ = recover() }()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		_ = b.mergeSharedProps(req, Props{})
	}()

	// Now write (would hang on wedged lock), then read.
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		b.Share("after", "value")
	}()
	select {
	case <-writeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("writer hung after a panicking callback; RLock leaked")
	}

	// Concurrent reads still work.
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { _ = recover() }()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			_ = b.mergeSharedProps(req, Props{})
		}()
	}

	readsDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(readsDone)
	}()
	select {
	case <-readsDone:
	case <-time.After(3 * time.Second):
		t.Fatal("readers hung after a panicking callback")
	}
}
