//go:build unix

package drivers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/velocitykode/velocity/contract"
)

// Conformance assertion: FileLock satisfies the contract lock interface.
// Kept under the unix build tag with the type it asserts.
var _ contract.CacheLock = (*FileLock)(nil)

// fileLockMetadata is the record of a held lock: its owner and when the
// hold ends. The record is the lock: a key whose record is missing,
// unparseable, without an owner, or past ExpiresAt is free. A record that
// exists but cannot be read leaves the state unknown, so an operation on
// it fails instead of treating the lock as free.
//
// An unparseable record is one whose writer stopped partway: the record
// is only read and written under the key's guard, so a reader never sees
// a write in progress, and a write that fails or a writer that crashes
// between the truncation and the write leaves it empty or cut short. Its
// TTL cannot be read, so treating it as held would keep the key locked
// until a ForceRelease; it is free instead, as it always was.
type fileLockMetadata struct {
	Owner     string     `json:"owner"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// held reports whether the record still holds the lock at now.
func (md fileLockMetadata) held(now time.Time) bool {
	return md.Owner != "" && md.ExpiresAt != nil && now.Before(*md.ExpiresAt)
}

// FileLock is a lock shared by every FileStore over the same directory,
// in this process or another on the same host. It satisfies the Lock
// interface for FileStore.
//
// The lock of a key is a record, <cache>/locks/<sha256(key)>.lock, naming
// its owner and the end of its TTL. Every read and change of the record
// (Get, Release, ForceRelease) runs under the write lock of the stripe
// the key hashes to: an exclusive flock(2) on one of the stable files
// under <cache>/locks/keys that the cache writes use (see
// lockKeyForWrite). Those files are never removed, so every caller locks
// the same inode and the check-and-set of a record is atomic across
// processes; the record itself is only ever read and changed under that
// lock, so removing it on release is safe.
//
// A lock is held until its owner releases it, it is force-released, or
// its TTL passes, as on the memory and redis drivers; a holder that
// crashes keeps the lock until the TTL passes.
//
// The stripe is shared with the cache writes of the keys that hash to
// it, so an acquisition by Get (and so Run) waits, up to fileKeyLockWait,
// while such a write holds it. Block bounds that wait by the time it has
// left, so it never acquires after its timeout.
type FileLock struct {
	store *fileLockStore
	key   string
	owner string
	ttl   time.Duration
}

// fileLockStore owns the per-FileStore lock record directory.
type fileLockStore struct {
	cache   *FileStore
	lockDir string
}

func newFileLockStore(cache *FileStore) (*fileLockStore, error) {
	lockDir := filepath.Join(cache.path, "locks")
	if err := os.MkdirAll(lockDir, cacheDirMode); err != nil {
		return nil, fmt.Errorf("velocity/cache: failed to create lock directory: %w", err)
	}
	return &fileLockStore{cache: cache, lockDir: lockDir}, nil
}

// pathFor is the record file of key.
func (s *fileLockStore) pathFor(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.lockDir, hex.EncodeToString(sum[:])+".lock")
}

// guard takes the write lock of the stripe key hashes to, waiting for
// another holder as a cache write does, honouring ctx. The returned func
// releases it.
func (s *fileLockStore) guard(ctx context.Context, key string) (func(), error) {
	return s.guardWithin(ctx, key, fileKeyLockWait)
}

// guardWithin is guard waiting at most wait for another holder.
func (s *fileLockStore) guardWithin(ctx context.Context, key string, wait time.Duration) (func(), error) {
	sum := sha256.Sum256([]byte(key))
	stripe := hex.EncodeToString(sum[:1])
	return s.cache.lockStripeWithin(ctx, stripe, "stripe "+stripe, wait)
}

// readMetadata reads the record at path. ok is false when the file is
// missing or unparseable, which means the lock is free. Any other read
// failure is returned as err: the record may name a live holder, so the
// caller must not treat the lock as free. The caller holds the key's
// guard.
func (s *fileLockStore) readMetadata(path string) (md fileLockMetadata, ok bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileLockMetadata{}, false, nil
	}
	if err != nil {
		return fileLockMetadata{}, false, err
	}
	if err := json.Unmarshal(data, &md); err != nil {
		return fileLockMetadata{}, false, nil
	}
	return md, true, nil
}

// writeMetadata replaces the record at path. The caller holds the key's
// guard.
func (s *fileLockStore) writeMetadata(path string, md fileLockMetadata) error {
	data, err := json.Marshal(md)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, cacheFileMode)
}

// removeMetadata removes the record at path. The caller holds the key's
// guard.
func (s *fileLockStore) removeMetadata(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
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
// It may wait up to fileKeyLockWait for the key's stripe (see FileLock).
func (l *FileLock) Get(ctx context.Context) bool {
	acquired, _ := l.GetWithErr(ctx)
	return acquired
}

// GetWithErr is the error-returning variant. The bool reports whether
// the lock was acquired; the error is non-nil on backend failure
// (the lock record exists but cannot be read, or cannot be written; the
// key's guard held for over fileKeyLockWait) or when the lock was
// constructed with a non-positive TTL (ErrInvalidLockTTL). A record that
// cannot be read is left untouched. Contention, including a second Get
// on a lock this instance already holds, is reported as (false, nil), as
// is a ctx cancelled before the lock was taken. Taking the record may
// wait up to fileKeyLockWait, honouring ctx, while a cache write of a key
// on the same stripe holds it.
//
// A zero/negative TTL is rejected: without expiry, a holder process
// that crashes between Get and Release would pin the lock forever.
// Forcing a positive TTL gives operators a reliable maximum-stale-lock
// window.
func (l *FileLock) GetWithErr(ctx context.Context) (bool, error) {
	return l.acquire(ctx, fileKeyLockWait)
}

// getBefore is the acquisition attempt of BlockLock: GetWithErr waiting
// for the key's stripe only until deadline. An attempt at or after
// deadline still takes a free stripe, without waiting for a held one.
func (l *FileLock) getBefore(ctx context.Context, deadline time.Time) bool {
	wait := min(max(time.Until(deadline), 0), fileKeyLockWait)
	acquired, _ := l.acquire(ctx, wait)
	return acquired
}

// acquire is GetWithErr waiting at most wait for the key's stripe.
func (l *FileLock) acquire(ctx context.Context, wait time.Duration) (bool, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return false, nil
		}
	}
	if l.ttl <= 0 {
		return false, ErrInvalidLockTTL
	}
	unlock, err := l.store.guardWithin(ctx, l.key, wait)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return false, nil
		}
		return false, fmt.Errorf("velocity/cache: lock guard: %w", err)
	}
	defer unlock()

	path := l.store.pathFor(l.key)
	now := time.Now()
	md, ok, err := l.store.readMetadata(path)
	if hook := l.store.cache.lockStepHook; hook != nil {
		hook("record-read")
	}
	if err != nil {
		return false, fmt.Errorf("velocity/cache: read lock metadata: %w", err)
	}
	if ok && md.held(now) {
		return false, nil
	}
	exp := now.Add(l.ttl)
	if err := l.store.writeMetadata(path, fileLockMetadata{Owner: l.owner, ExpiresAt: &exp}); err != nil {
		return false, fmt.Errorf("velocity/cache: write lock metadata: %w", err)
	}
	return true, nil
}

// Release releases the lock only if the current instance is the owner:
// the record names this owner. Returns true if released; false when the
// record names another owner, is gone, or cannot be read (ownership is
// then unknown and the record is left in place). A lock restored with
// RestoreLock releases the lock its owner ID holds.
func (l *FileLock) Release(ctx context.Context) bool {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return false
		}
	}
	unlock, err := l.store.guard(ctx, l.key)
	if err != nil {
		return false
	}
	defer unlock()

	path := l.store.pathFor(l.key)
	md, ok, err := l.store.readMetadata(path)
	if err != nil || !ok || md.Owner != l.owner {
		return false
	}
	return l.store.removeMetadata(path) == nil
}

// ForceRelease deletes the lock record regardless of owner, so a
// subsequent Get from any caller can acquire.
func (l *FileLock) ForceRelease(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	unlock, err := l.store.guard(ctx, l.key)
	if err != nil {
		return fmt.Errorf("velocity/cache: lock guard: %w", err)
	}
	defer unlock()
	if err := l.store.removeMetadata(l.store.pathFor(l.key)); err != nil {
		return fmt.Errorf("velocity/cache: remove lock metadata: %w", err)
	}
	return nil
}

// Run acquires the lock, runs the callback, and releases the lock.
// Returns ErrLockNotAcquired if the lock cannot be acquired. The lock
// is released even if the callback panics; the panic propagates.
func (l *FileLock) Run(ctx context.Context, callback func()) error {
	return RunLock(ctx, l, callback)
}

// Block polls for the lock up to timeout (every 100ms) then runs the
// callback under the lock. Returns ErrLockTimeout on timeout, or
// ctx.Err() if ctx is cancelled before acquisition. An attempt waits for
// the key's stripe only for the time Block has left, so a cache write
// holding the stripe past the timeout ends in ErrLockTimeout and the
// callback does not run.
func (l *FileLock) Block(ctx context.Context, timeout time.Duration, callback func()) error {
	return BlockLock(ctx, l, timeout, callback)
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
		store, err := newFileLockStore(s)
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
