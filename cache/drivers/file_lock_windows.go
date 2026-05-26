//go:build !unix

package drivers

import "time"

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
