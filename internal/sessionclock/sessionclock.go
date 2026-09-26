// Package sessionclock is the one wall-clock source the session lifetime
// policy reads: the session cookie's issue and expiry checks, the session
// scheme's activity refresh and the server session stores' expiry checks
// all take their "now" from here, so they agree on one clock.
//
// Production code never changes it. Set exists so tests can walk a session
// through hours of idle and active time without sleeping; it replaces the
// clock for every reader at once, so tests that call it must not run in
// parallel with other tests that read session time.
package sessionclock

import (
	"sync/atomic"
	"time"
)

var current atomic.Pointer[func() time.Time]

// Now returns the current session time: time.Now unless a test replaced it.
func Now() time.Time {
	if fn := current.Load(); fn != nil {
		return (*fn)()
	}
	return time.Now()
}

// Set replaces the session clock with now and returns a function that
// restores the previous clock. A nil now restores time.Now. Tests only.
func Set(now func() time.Time) (restore func()) {
	var next *func() time.Time
	if now != nil {
		next = &now
	}
	prev := current.Swap(next)
	return func() { current.Store(prev) }
}
