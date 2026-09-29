// Package latency holds the one slow-operation rule the framework's
// operation log lines share: the key and unit an operation's wall time is
// written in, and when an operation counts as slow. The ORM's statement
// log and the gRPC logging interceptor both follow it, so a slow query
// and a slow call read the same way in the log.
//
// It imports only the standard library.
package latency

import "time"

// Key is the log key an operation's wall time is written under, in whole
// milliseconds (see Millis).
const Key = "duration_ms"

// Millis returns d in whole milliseconds, the unit Key is written in.
func Millis(d time.Duration) int64 {
	return d.Milliseconds()
}

// Slow reports whether an operation that took d is slow under threshold:
// it ran strictly longer than a positive threshold. A zero or negative
// threshold disables the rule, so nothing is slow.
func Slow(d, threshold time.Duration) bool {
	return threshold > 0 && d > threshold
}
