package drivers

import (
	"context"
	"time"
)

// lockRunner is the minimal lock surface RunLock and BlockLock need. Every
// built-in lock (MemoryLock, FileLock, RedisLock) satisfies it.
type lockRunner interface {
	Get(ctx context.Context) bool
	Release(ctx context.Context) bool
}

// lockBlockRetryInterval is how often BlockLock re-attempts acquisition while
// waiting for the lock to free up.
const lockBlockRetryInterval = 100 * time.Millisecond

// RunLock acquires the lock, runs the callback, and releases the lock.
// Returns ErrLockNotAcquired if the lock cannot be acquired. The lock is
// released even if the callback panics; the panic propagates.
func RunLock(ctx context.Context, l lockRunner, callback func()) error {
	if !l.Get(ctx) {
		return ErrLockNotAcquired
	}
	defer l.Release(ctx)
	callback()
	return nil
}

// BlockLock polls for the lock up to timeout (every 100ms) then runs the
// callback under the lock. Returns ErrLockTimeout on timeout, or ctx.Err()
// if ctx is cancelled before acquisition. The lock is released even if the
// callback panics; the panic propagates. A nil ctx is tolerated (no
// cancellation, plain sleep between retries).
func BlockLock(ctx context.Context, l lockRunner, timeout time.Duration, callback func()) error {
	return blockLock(ctx, func(ctx context.Context, _ time.Time) bool { return l.Get(ctx) }, l.Release, timeout, callback)
}

// blockLock is the retry loop of BlockLock. probe makes one acquisition
// attempt and receives the deadline, so an attempt that can wait (a
// FileLock waiting for its key's stripe) can bound that wait; release
// releases a lock probe took.
func blockLock(ctx context.Context, probe func(context.Context, time.Time) bool, release func(context.Context) bool, timeout time.Duration, callback func()) error {
	deadline := time.Now().Add(timeout)

	for {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}

		if probe(ctx, deadline) {
			defer release(ctx)
			callback()
			return nil
		}

		if time.Now().After(deadline) {
			return ErrLockTimeout
		}

		// Sleep but wake early on ctx cancellation so Block returns promptly.
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(lockBlockRetryInterval):
			}
		} else {
			time.Sleep(lockBlockRetryInterval)
		}
	}
}
