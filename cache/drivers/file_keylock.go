//go:build unix

package drivers

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// fileKeyLockWait bounds how long a write waits for another holder of its
// key's write lock. A holder keeps it for one read and one write of one
// cache file, so the bound is only reached behind a holder that hangs
// (a stalled filesystem); a crashed holder's flock is dropped by the kernel.
const fileKeyLockWait = 30 * time.Second

// fileKeyLockPollMax caps the back-off between two attempts on a held
// write lock.
const fileKeyLockPollMax = 10 * time.Millisecond

// lockKeyForWrite takes the write lock of key: an exclusive flock(2) on
// one of 256 lock files under <cache>/locks/keys, picked by the first byte
// of the hash of the prefixed key. Every FileStore write of a key (Put, Add,
// Forever, Forget, Increment, CompareAndSwap and the set operations) holds
// it around its read and write, so writes of one key from every FileStore
// sharing the directory, in this process or another, never interleave.
//
// The lock files are stable: they are never removed (the expiry sweep and
// Flush skip their directory), so every holder locks the same inode. A
// lock file unlinked from outside is detected after the flock (the path no
// longer names the locked file) and the lock is taken again on the new
// file. A striped file is shared by the keys that hash to it, which only
// serializes their writes.
//
// A held lock is contention and is waited for, honouring ctx, for up to
// fileKeyLockWait; the wait ending is an error, never a report on a value
// the caller did not compare. The returned func releases the lock.
func (s *FileStore) lockKeyForWrite(ctx context.Context, key string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sum := sha256.Sum256([]byte(s.prefixedKey(key)))
	path := filepath.Join(s.keyLockDir(), fmt.Sprintf("%02x.lock", sum[0]))

	deadline := time.Now().Add(fileKeyLockWait)
	pause := time.Millisecond
	for {
		fd, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, cacheFileMode)
		if errors.Is(err, fs.ErrNotExist) {
			// First write, or the directory was removed from outside.
			if merr := os.MkdirAll(s.keyLockDir(), cacheDirMode); merr != nil {
				return nil, fmt.Errorf("velocity/cache: create key lock directory: %w", merr)
			}
			fd, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE, cacheFileMode)
		}
		if err != nil {
			return nil, fmt.Errorf("velocity/cache: open key lock file: %w", err)
		}
		err = unix.Flock(int(fd.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if sameFile(fd, path) {
				return func() {
					_ = unix.Flock(int(fd.Fd()), unix.LOCK_UN)
					_ = fd.Close()
				}, nil
			}
			// The file was unlinked (and maybe recreated) between the open
			// and the flock: the lock guards nothing. Take it again.
			_ = unix.Flock(int(fd.Fd()), unix.LOCK_UN)
			_ = fd.Close()
			continue
		}
		_ = fd.Close()
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return nil, fmt.Errorf("velocity/cache: flock key lock: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("velocity/cache: key %q write lock held for over %v: %w", key, fileKeyLockWait, ErrLockTimeout)
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if pause *= 2; pause > fileKeyLockPollMax {
			pause = fileKeyLockPollMax
		}
	}
}

// sameFile reports whether path still names the file open as fd.
func sameFile(fd *os.File, path string) bool {
	held, err := fd.Stat()
	if err != nil {
		return false
	}
	current, err := os.Stat(path)
	if err != nil {
		return false
	}
	return os.SameFile(held, current)
}
