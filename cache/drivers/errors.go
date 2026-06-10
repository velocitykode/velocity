package drivers

import "errors"

// ErrCannotFlushUnprefixed is returned by a remote cache store's Flush when
// the store was configured with an empty prefix. For Redis, SCAN with pattern
// "*" iterates the entire database and DEL deletes every key returned, which
// on a shared instance wipes every other application's data. Operators who
// genuinely want that behaviour must opt in via the driver's FlushAllUnsafe.
//
// The sentinel lives in the light part of cache/drivers (no go-redis
// dependency) so cache (core) can re-export it via cache.ErrCannotFlushUnprefixed
// without importing the redis leaf, while the redis driver package returns it.
var ErrCannotFlushUnprefixed = errors.New("velocity/cache: refusing to Flush a Redis store with empty prefix (would wipe entire DB); set Prefix or call FlushAllUnsafe")

// ErrValueTooLarge is returned by Put/Add/Forever (and their batch variants)
// when a store is configured with a per-value size cap and a single
// serialized value exceeds it. The cap is opt-in: stores default to
// unlimited, so only operators that set MaxValueBytes see this error.
// Callers should errors.Is against this sentinel; the wrapping error
// carries the offending and maximum sizes.
var ErrValueTooLarge = errors.New("velocity/cache: value exceeds configured MaxValueBytes cap")
