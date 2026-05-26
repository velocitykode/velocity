package drivers

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var releaseLockScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
    return redis.call("del", KEYS[1])
else
    return 0
end
`)

// RedisLock implements a Redis-based distributed lock.
type RedisLock struct {
	client *redis.Client
	key    string
	owner  string
	ttl    time.Duration
}

// NewRedisLock creates a new RedisLock instance.
func NewRedisLock(client *redis.Client, key string, owner string, ttl time.Duration) *RedisLock {
	return &RedisLock{
		client: client,
		key:    key,
		owner:  owner,
		ttl:    ttl,
	}
}

// Get attempts to acquire the lock. Returns true if the lock was acquired.
// The caller's ctx is propagated to the underlying Redis call so cancellation
// aborts the SETNX in-flight rather than blocking on a slow network.
//
// Get collapses contention (SETNX returned false) and backend errors
// (network reset, AUTH failure, OOM) into a single false return. Callers
// that need to distinguish the two MUST use GetWithErr; treating every
// false from Get as "another host owns this lock" hides Redis outages
// behind silent skip behaviour. The scheduler's cacheLocker adapter
// uses GetWithErr for exactly this reason.
func (l *RedisLock) Get(ctx context.Context) bool {
	acquired, _ := l.GetWithErr(ctx)
	return acquired
}

// GetWithErr is the error-returning variant. The bool reports SETNX
// outcome (true iff Redis stored the key); the error is the Redis
// client error verbatim when the call failed. A non-nil error means
// the SETNX result is undefined; callers MUST NOT interpret a (false,
// err != nil) return as "another host owns the lock".
//
// Typical error shapes go-redis surfaces: ctx.Err() if the caller's
// context cancels; net.OpError on dropped connections; redis.Error /
// proto errors on AUTH / NOAUTH / OOM / READONLY. All are returned
// verbatim so callers can inspect via errors.Is / errors.As.
func (l *RedisLock) GetWithErr(ctx context.Context) (bool, error) {
	result, err := l.client.SetNX(ctx, l.key, l.owner, l.ttl).Result()
	if err != nil {
		return false, err
	}
	return result, nil
}

// Release releases the lock only if the current instance is the owner.
// Returns true if the lock was successfully released.
// The caller's ctx is propagated to the underlying Redis EVAL.
func (l *RedisLock) Release(ctx context.Context) bool {
	result, err := releaseLockScript.Run(ctx, l.client, []string{l.key}, l.owner).Int64()
	return err == nil && result == 1
}

// Run acquires the lock, runs the callback, and releases the lock.
// Returns ErrLockNotAcquired if the lock cannot be acquired.
// If the callback panics, the lock is still released and the panic propagates.
func (l *RedisLock) Run(ctx context.Context, callback func()) error {
	if !l.Get(ctx) {
		return ErrLockNotAcquired
	}
	// Release always runs, even on panic. We deliberately re-use the caller's
	// ctx — if they cancelled it, releasing through a dead ctx is acceptable
	// because the lock will eventually expire on its TTL.
	defer l.Release(ctx)

	callback()
	return nil
}

// Block attempts to acquire the lock within the given timeout, retrying every 100ms.
// Once acquired, it runs the callback and releases the lock.
// Returns ErrLockTimeout if the lock cannot be acquired within the timeout,
// or ctx.Err() if ctx is cancelled before acquisition.
// If the callback panics, the lock is still released and the panic propagates.
func (l *RedisLock) Block(ctx context.Context, timeout time.Duration, callback func()) error {
	deadline := time.Now().Add(timeout)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		if l.Get(ctx) {
			defer l.Release(ctx)
			callback()
			return nil
		}

		if time.Now().After(deadline) {
			return ErrLockTimeout
		}

		// Sleep but wake early if ctx is cancelled so Block honors ctx promptly.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Owner returns the owner identifier of this lock.
func (l *RedisLock) Owner() string {
	return l.owner
}

// ForceRelease deletes the lock key without checking the owner.
// The caller's ctx is propagated to the underlying Redis DEL.
func (l *RedisLock) ForceRelease(ctx context.Context) error {
	return l.client.Del(ctx, l.key).Err()
}

// Lock creates a new lock for the given key with an optional TTL.
func (s *RedisStore) Lock(key string, ttl ...time.Duration) Lock {
	lockTTL := time.Duration(0)
	if len(ttl) > 0 {
		lockTTL = ttl[0]
	}
	owner := uuid.New().String()
	return NewRedisLock(s.client, s.prefixedKey("lock:"+key), owner, lockTTL)
}

// RestoreLock restores a lock instance for the given key and owner without acquiring it.
func (s *RedisStore) RestoreLock(key string, owner string) Lock {
	return NewRedisLock(s.client, s.prefixedKey("lock:"+key), owner, 0)
}
