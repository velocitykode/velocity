// Package subtest is test infrastructure: excluded by its directory name.
package subtest

import "sync"

var mu sync.Mutex

// Call calls fn under a lock; the checker ignores this package.
func Call(fn func()) {
	mu.Lock()
	defer mu.Unlock()
	fn()
}
