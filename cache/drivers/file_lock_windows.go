//go:build !unix

package drivers

import (
	"context"
	"time"
)

// ensureLockStore on non-unix platforms returns ErrLockNotSupported so
// callers fall through Manager.Lock's nil branch. File-based flock(2)
// is not portable to Windows; operators on Windows should use the
// memory or redis driver for distributed locking.
func (s *FileStore) ensureLockStore() (*fileLockStore, error) {
	return nil, ErrLockNotSupported
}

// fileLockStore is a stub on non-unix platforms; Lock/RestoreLock
// return nil so callers must check before use.
type fileLockStore struct{}

// Lock returns nil on non-unix platforms.
func (s *FileStore) Lock(key string, ttl ...time.Duration) Lock {
	return nil
}

// RestoreLock returns nil on non-unix platforms.
func (s *FileStore) RestoreLock(key string, owner string) Lock {
	return nil
}

// lockKeyForWrite on non-unix platforms returns ErrLockNotSupported: there
// is no flock(2) to serialize writes of a key across processes. The plain
// writes (Put, Add, Forever, Forget, Increment) proceed under the store
// mutex alone, as they always have; CompareAndSwapCtx and the set
// operations, whose contracts need the cross-process lock, return the
// error.
func (s *FileStore) lockKeyForWrite(ctx context.Context, key string) (func(), error) {
	return nil, ErrLockNotSupported
}

// lockStripe on non-unix platforms returns ErrLockNotSupported, as
// lockKeyForWrite does; Flush then removes entries under the store mutex
// alone, as it always has.
func (s *FileStore) lockStripe(ctx context.Context, stripe, what string) (func(), error) {
	return nil, ErrLockNotSupported
}

// flockStripe on non-unix platforms returns ErrLockNotSupported; the
// temp-file cleanup then falls back to the age check alone.
func (s *FileStore) flockStripe(stripe string) (unlock func(), busy bool, err error) {
	return nil, false, ErrLockNotSupported
}
