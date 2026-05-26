//go:build unix

package drivers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

// fileLockMetadata is written to the on-disk lock file so that a
// recovering ForceRelease (or a peer process inspecting state) can see
// the owner and expiry. The metadata is purely informational - actual
// mutual exclusion is enforced by flock(2). On a peer-process crash the
// kernel drops the LOCK_EX automatically, so an expired lock file
// without a holder is reacquirable immediately.
type fileLockMetadata struct {
	Owner     string     `json:"owner"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// FileLock is a process-aware file lock backed by flock(2). It satisfies
// the Lock interface for FileStore. The lock state lives in a file under
// <cache>/locks/<sha256(key)>.lock; flock provides mutual exclusion
// across processes on the same host and within a single process across
// goroutines. TTL is advisory: it lets a holder express an upper bound
// on how long it will hold the lock so manual recovery can be scripted,
// but the kernel reclaims the flock when the holding process exits.
type FileLock struct {
	store *fileLockStore
	key   string
	owner string
	ttl   time.Duration

	mu sync.Mutex
	// fd holds the open *os.File while the lock is acquired. The flock
	// is associated with the file descriptor; closing fd releases the
	// kernel-level lock automatically as a defence in depth.
	fd *os.File
}

// fileLockStore owns the per-FileStore lock directory and an in-process
// holders map that records which goroutine currently owns each key.
// flock is a process-level primitive on POSIX, so two goroutines in
// one process can each hold LOCK_EX on the same inode -- the holders
// map closes that hole and lets ForceRelease safely steal a lock from
// a peer goroutine without trying to "force-unlock" a sync.Mutex
// (which is undefined behaviour).
type fileLockStore struct {
	lockDir string

	mu      sync.Mutex
	holders map[string]string // key -> owner ID of the in-process holder
}

func newFileLockStore(cacheDir string) (*fileLockStore, error) {
	lockDir := filepath.Join(cacheDir, "locks")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return nil, fmt.Errorf("velocity/cache: failed to create lock directory: %w", err)
	}
	return &fileLockStore{
		lockDir: lockDir,
		holders: make(map[string]string),
	}, nil
}

// tryClaim attempts to record `owner` as the in-process holder of `key`.
// Returns true if claimed (key was unheld); false if another goroutine
// in the same process already holds it.
func (s *fileLockStore) tryClaim(key, owner string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, held := s.holders[key]; held {
		return false
	}
	s.holders[key] = owner
	return true
}

// release drops the in-process holder record only if the caller owns
// it. Returns true if the entry was removed.
func (s *fileLockStore) release(key, owner string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, held := s.holders[key]; held && existing == owner {
		delete(s.holders, key)
		return true
	}
	return false
}

// forceRelease drops the in-process holder record regardless of owner.
// Used by ForceRelease to steal a stale lock from a peer goroutine.
func (s *fileLockStore) forceRelease(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.holders, key)
}

// holder reports the current in-process owner of key (or "" if unheld).
func (s *fileLockStore) holder(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holders[key]
}

func (s *fileLockStore) pathFor(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.lockDir, hex.EncodeToString(sum[:])+".lock")
}

// readMetadata best-effort reads owner/expiry from the on-disk lock
// file. Returns ok=false if the file is missing or unparseable; callers
// MUST NOT rely on the metadata for mutual exclusion (use flock).
func (s *fileLockStore) readMetadata(path string) (fileLockMetadata, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fileLockMetadata{}, false
	}
	var md fileLockMetadata
	if err := json.Unmarshal(data, &md); err != nil {
		return fileLockMetadata{}, false
	}
	return md, true
}

func (s *fileLockStore) writeMetadata(path string, md fileLockMetadata) error {
	data, err := json.Marshal(md)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// NewFileLock creates a new FileLock bound to the given lock store.
func NewFileLock(store *fileLockStore, key, owner string, ttl time.Duration) *FileLock {
	return &FileLock{
		store: store,
		key:   key,
		owner: owner,
		ttl:   ttl,
	}
}

// Get attempts to acquire the lock. Returns true if the lock was acquired.
func (l *FileLock) Get(ctx context.Context) bool {
	acquired, _ := l.GetWithErr(ctx)
	return acquired
}

// GetWithErr is the error-returning variant. The bool reports whether
// the lock was acquired; the error is non-nil on backend failure
// (cannot open the lock file, etc.) or when the lock was constructed
// with a non-positive TTL (ErrInvalidLockTTL). Contention is reported
// as (false, nil).
//
// A zero/negative TTL is rejected: without expiry, a holder process
// that crashes between Get and Release pins the on-disk lock file
// forever; subsequent acquirers see the metadata "still held until
// ExpiresAt" check (which is now `ExpiresAt == nil` -> treated as
// "no declared end") and may keep blocking. Forcing a positive TTL
// gives operators a reliable maximum-stale-lock window.
func (l *FileLock) GetWithErr(ctx context.Context) (bool, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return false, nil
		}
	}
	if l.ttl <= 0 {
		return false, ErrInvalidLockTTL
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fd != nil {
		// Already held by this FileLock instance. Treat as "not acquired
		// by this call" so Get matches the MemoryLock contract.
		return false, nil
	}

	// Claim the in-process holder slot BEFORE flock. flock is a
	// process-level primitive on Linux/macOS; two goroutines in one
	// process can each hold LOCK_EX on the same inode, so we need an
	// intra-process gate too. tryClaim is a check-and-set under the
	// store's holders mutex so concurrent goroutines see exactly one
	// winner per key. The matching release runs in Release /
	// ForceRelease.
	if !l.store.tryClaim(l.key, l.owner) {
		return false, nil
	}

	path := l.store.pathFor(l.key)
	fd, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		l.store.release(l.key, l.owner)
		return false, fmt.Errorf("velocity/cache: open lock file: %w", err)
	}

	// Non-blocking exclusive flock. EWOULDBLOCK == contention.
	if err := unix.Flock(int(fd.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = fd.Close()
		l.store.release(l.key, l.owner)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return false, nil
		}
		return false, fmt.Errorf("velocity/cache: flock: %w", err)
	}

	// Before declaring success, honour any expiry recorded by a prior
	// holder: if a previous owner wrote a future ExpiresAt that has not
	// elapsed, refuse the acquire even though flock succeeded (which
	// would only happen if the previous holder crashed without writing
	// "released" metadata - i.e. a stale lock that we treat as still
	// held until its declared TTL elapses, mirroring MemoryLock).
	if md, ok := l.store.readMetadata(path); ok && md.Owner != "" && md.Owner != l.owner {
		if md.ExpiresAt != nil && time.Now().Before(*md.ExpiresAt) {
			_ = unix.Flock(int(fd.Fd()), unix.LOCK_UN)
			_ = fd.Close()
			l.store.release(l.key, l.owner)
			return false, nil
		}
	}

	md := fileLockMetadata{Owner: l.owner}
	if l.ttl > 0 {
		exp := time.Now().Add(l.ttl)
		md.ExpiresAt = &exp
	}
	if err := l.store.writeMetadata(path, md); err != nil {
		_ = unix.Flock(int(fd.Fd()), unix.LOCK_UN)
		_ = fd.Close()
		l.store.release(l.key, l.owner)
		return false, fmt.Errorf("velocity/cache: write lock metadata: %w", err)
	}

	l.fd = fd
	return true, nil
}

// Release releases the lock only if the current instance is the owner.
// Returns true if released. Drops both the on-disk flock and the
// intra-process holder record.
func (l *FileLock) Release(ctx context.Context) bool {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return false
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fd == nil {
		// Caller never acquired (or already released). Check on-disk
		// metadata for owner match: if a different process or a
		// restored FileLock instance owns it, we must refuse.
		path := l.store.pathFor(l.key)
		md, ok := l.store.readMetadata(path)
		if !ok || md.Owner != l.owner {
			return false
		}
		// Owner match but we have no fd - this is a restored lock.
		// Drop the on-disk record so the next Get can acquire. The
		// flock itself isn't held by us; the previous holder's flock
		// has either been released (typical) or its process exited
		// and the kernel reclaimed it.
		_ = os.Remove(path)
		return true
	}

	path := l.store.pathFor(l.key)
	md, ok := l.store.readMetadata(path)
	if ok && md.Owner != l.owner {
		// Metadata says someone else owns it (race after a stale
		// recovery), even though we hold the fd. Refuse.
		return false
	}
	_ = os.Remove(path)
	_ = unix.Flock(int(l.fd.Fd()), unix.LOCK_UN)
	_ = l.fd.Close()
	l.fd = nil
	l.store.release(l.key, l.owner)
	return true
}

// ForceRelease deletes the lock state regardless of owner. Drops the
// on-disk lock file and the in-process holder record so a subsequent
// Get from any caller can acquire. If THIS instance currently holds
// the fd it is released too; otherwise only the bookkeeping is
// touched and the original holder's fd will become a no-op release
// when it eventually Releases (the file is already gone).
func (l *FileLock) ForceRelease(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	path := l.store.pathFor(l.key)
	_ = os.Remove(path)
	if l.fd != nil {
		_ = unix.Flock(int(l.fd.Fd()), unix.LOCK_UN)
		_ = l.fd.Close()
		l.fd = nil
	}
	// forceRelease drops the in-process holder record unconditionally
	// so a stale peer's hold no longer blocks future tryClaim calls.
	l.store.forceRelease(l.key)
	return nil
}

// Run acquires the lock, runs the callback, and releases the lock.
// Returns ErrLockNotAcquired if the lock cannot be acquired. The lock
// is released even if the callback panics; the panic propagates.
func (l *FileLock) Run(ctx context.Context, callback func()) error {
	if !l.Get(ctx) {
		return ErrLockNotAcquired
	}
	defer l.Release(ctx)
	callback()
	return nil
}

// Block polls for the lock up to timeout (every 100ms) then runs the
// callback under the lock. Returns ErrLockTimeout on timeout, or
// ctx.Err() if ctx is cancelled before acquisition.
func (l *FileLock) Block(ctx context.Context, timeout time.Duration, callback func()) error {
	deadline := time.Now().Add(timeout)

	for {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if l.Get(ctx) {
			defer l.Release(ctx)
			callback()
			return nil
		}
		if time.Now().After(deadline) {
			return ErrLockTimeout
		}
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// Owner returns the owner identifier of this lock instance.
func (l *FileLock) Owner() string {
	return l.owner
}

// Lock creates a new FileLock for the given key with an optional TTL.
// The first call lazily creates the locks/ directory under the cache
// path; subsequent calls reuse the cached fileLockStore. Returns a Lock
// that always works on FileStore -- the historical "Manager.Lock returns
// nil for file driver" pitfall is gone.
func (s *FileStore) Lock(key string, ttl ...time.Duration) Lock {
	lockTTL := time.Duration(0)
	if len(ttl) > 0 {
		lockTTL = ttl[0]
	}
	store, err := s.ensureLockStore()
	if err != nil {
		// Surfacing the error here would require a sentinel "broken"
		// Lock; instead we mirror the existing pattern by returning
		// nil. The startup MkdirAll happens once per cache root in
		// NewFileStoreWithOptions, so this branch only fires under
		// catastrophic filesystem failure.
		return nil
	}
	owner := uuid.New().String()
	return NewFileLock(store, PrefixKey(s.prefix, "lock:"+key), owner, lockTTL)
}

// RestoreLock restores a FileLock instance for the given key and owner
// without acquiring. Useful when a long-running job persists its lock
// owner ID and resumes after a process restart.
func (s *FileStore) RestoreLock(key string, owner string) Lock {
	store, err := s.ensureLockStore()
	if err != nil {
		return nil
	}
	return NewFileLock(store, PrefixKey(s.prefix, "lock:"+key), owner, 0)
}

// ensureLockStore lazily initialises the FileStore's fileLockStore on
// first Lock call. Returns the cached instance on subsequent calls.
func (s *FileStore) ensureLockStore() (*fileLockStore, error) {
	s.lockOnce.Do(func() {
		store, err := newFileLockStore(s.path)
		if err != nil {
			s.lockErr = err
			return
		}
		s.lockStore = store
	})
	if s.lockErr != nil {
		return nil, s.lockErr
	}
	return s.lockStore, nil
}
