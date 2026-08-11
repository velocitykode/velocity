package auth

import (
	"sync"
	"testing"
	"time"
)

// panicPolicy is a Policy whose Authorize() should never be reached in the
// E-04 regression tests because the panicking Before callback fires first.
type panicPolicy struct{}

func (panicPolicy) Authorize(user Authenticatable, action string, resource interface{}) bool {
	return false
}

// TestAuthorizePolicy_BeforePanicDoesNotLeakRLock is the regression test for
// security finding E-04. AuthorizePolicy previously held g.mu.RLock while
// invoking user-supplied Before callbacks; a panic from those callbacks
// bypassed the explicit RUnlock and permanently wedged the Access's RWMutex,
// hanging every subsequent Define / RegisterPolicy / Before / After writer
// (and eventually every reader queued behind that writer).
//
// This test:
//  1. Registers a Before callback that panics.
//  2. Registers a policy so AuthorizePolicy reaches the before-loop.
//  3. Calls AuthorizePolicy from a goroutine, recovering the panic locally.
//  4. From a second goroutine, calls Define() with a 2-second timeout.
//  5. Asserts the Define() call completes (the lock was released).
//
// Repeats 10 times to confirm no cumulative goroutine leak.
func TestAuthorizePolicy_BeforePanicDoesNotLeakRLock(t *testing.T) {
	for iter := 0; iter < 10; iter++ {
		access := NewAccess()
		access.RegisterPolicy("post", panicPolicy{})
		access.Before(func(user Authenticatable, ability string, args ...interface{}) *bool {
			panic("E-04 regression: simulate before-callback panic")
		})

		user := &mockUser{id: 1}
		post := &mockPost{ID: 1, AuthorID: 1}

		// Goroutine A: trigger the panic.
		panicDone := make(chan struct{})
		go func() {
			defer close(panicDone)
			defer func() {
				_ = recover() // expected, ignored
			}()
			_ = access.AuthorizePolicy(user, "post", "edit", post)
		}()

		select {
		case <-panicDone:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: AuthorizePolicy goroutine did not finish in 2s", iter)
		}

		// Goroutine B: call a writer. Before the fix this hung forever.
		defineDone := make(chan struct{})
		go func() {
			defer close(defineDone)
			access.Define("x", func(user Authenticatable, args ...interface{}) bool {
				return true
			})
		}()

		select {
		case <-defineDone:
			// ok, lock was released
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: Define() hung for 2s after a panicking Before callback; RLock was leaked", iter)
		}
	}
}

// TestAuthorizePolicy_PanicAllowsConcurrentAuthorization ensures readers can
// continue to authorize after a panicking sibling Before callback. Without
// the fix, queued writers behind the wedged RLock would also block readers
// (RWMutex contract forbids new readers when a writer is queued), so
// authorization would stop responding process-wide.
func TestAuthorizePolicy_PanicAllowsConcurrentAuthorization(t *testing.T) {
	access := NewAccess()
	access.RegisterPolicy("post", PolicyFunc(func(user Authenticatable, action string, resource interface{}) bool {
		return true
	}))

	// Register a panicking Before first, then a regular Define attempt
	// after the panic to prove the writer path is unblocked.
	access.Before(func(user Authenticatable, ability string, args ...interface{}) *bool {
		panic("E-04 regression: panic in before")
	})

	user := &mockUser{id: 1}
	post := &mockPost{ID: 1, AuthorID: 1}

	// Trigger the panic once.
	func() {
		defer func() { _ = recover() }()
		_ = access.AuthorizePolicy(user, "post", "edit", post)
	}()

	// Writer must not hang.
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		access.Define("after", func(u Authenticatable, args ...interface{}) bool { return true })
	}()
	select {
	case <-writeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("writer hung after a panicking Before callback; RLock leaked")
	}

	// Concurrent reads still work. Each will panic-and-recover from the
	// same Before callback, but the lock must remain healthy throughout.
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { _ = recover() }()
			_ = access.AuthorizePolicy(user, "post", "edit", post)
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
		t.Fatal("readers hung after a panicking Before callback")
	}
}
