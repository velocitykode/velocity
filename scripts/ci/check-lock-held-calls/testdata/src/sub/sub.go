// Package sub checks reach across packages.
package sub

import "sync"

var mu sync.Mutex

// Locked calls fn under a lock: reported when sub is named, not when only
// a package importing it is.
func Locked(fn func()) {
	mu.Lock()
	defer mu.Unlock()
	fn() // want func
}

// CallHook calls a func value: user code.
func CallHook(fn func()) { fn() }

// Pure calls no user code.
func Pure() int { return 1 }

// Wrap returns a func that calls fn: building it runs no user code.
func Wrap(fn func()) func() {
	return func() { fn() }
}
