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
	"strings"
	"sync"
	"time"

	"github.com/velocitykode/velocity/async"
)

// DefaultFileCleanupInterval is the default period between expired-file sweeps.
const DefaultFileCleanupInterval = 5 * time.Minute

// fileUnreadableGrace is how long an unreadable file (corrupt, legacy-format,
// or an AddCtx create on a filesystem without hard links, whose O_EXCL file
// is empty until its payload lands) or a write's temp file must have been
// untouched before cleanup will purge it. A freshly-modified one may be an
// in-flight write by another FileStore instance or process sharing the
// path. Purging only files older than this grace avoids deleting such live
// writes while still self-healing genuinely corrupt/legacy/crashed entries.
// The grace dwarfs any real write window yet keeps cleanup reasonably prompt.
const fileUnreadableGrace = time.Minute

// fileTempMarker marks a write's temp file: <entry name>.tmp-<random>, in
// the entry's shard directory. Every write of an item is written whole to
// a temp file and then renamed over (or, for AddCtx, hard-linked to) the
// entry name, so a read in any process sees the old item or the new one,
// never a partial file. Entry names are hex hashes and never contain the
// marker; the sweep and Flush treat temp files as in-flight writes, never
// as entries (see isTempFile).
const fileTempMarker = ".tmp-"

// isTempFile reports whether path is a write's temp file.
func isTempFile(path string) bool {
	return strings.Contains(filepath.Base(path), fileTempMarker)
}

// writeTempFile writes data whole to a new temp file beside path and
// returns its name. The caller renames or links it into place and removes
// it on failure.
func writeTempFile(path string, data []byte) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+fileTempMarker+"*")
	if err != nil {
		return "", fmt.Errorf("velocity/cache: failed to create temp cache file: %w", err)
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, cacheFileMode)
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("velocity/cache: failed to write cache file: %w", werr)
	}
	return tmp, nil
}

// replaceFile makes data the content of path in one step: written whole to
// a temp file in the same directory, then renamed over path. A reader in
// any process opens either the previous file or the new one.
func replaceFile(path string, data []byte) error {
	tmp, err := writeTempFile(path, data)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("velocity/cache: failed to write cache file: %w", err)
	}
	return nil
}

// createFileExclusive creates path with data only when path does not
// exist, in one step: written whole to a temp file, then hard-linked to
// path, which the kernel refuses when path exists, so exactly one creator
// wins and no reader ever sees the file partly written. created is false
// when path already exists. On a filesystem without hard links it falls
// back to an O_EXCL create followed by the write, whose file is empty
// until the payload lands (the zero-byte marker AddCtx honours).
func createFileExclusive(path string, data []byte) (created bool, err error) {
	tmp, err := writeTempFile(path, data)
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp) }()
	lerr := os.Link(tmp, path)
	if lerr == nil {
		return true, nil
	}
	if errors.Is(lerr, fs.ErrExist) {
		return false, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, cacheFileMode)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("velocity/cache: failed to create cache file: %w", err)
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		_ = os.Remove(path)
		return false, fmt.Errorf("velocity/cache: failed to write cache file: %w", werr)
	}
	if cerr != nil {
		return false, fmt.Errorf("velocity/cache: failed to close cache file: %w", cerr)
	}
	return true, nil
}

// cacheFileMode / cacheDirMode are the secret-tier permissions used for
// every file the FileStore writes. Cached values may carry session
// payloads, user data, or auth state, so other local users must not be
// able to read them.
const (
	cacheFileMode os.FileMode = 0o600
	cacheDirMode  os.FileMode = 0o700
)

// FileStore implements a file-based cache store
type FileStore struct {
	mu              sync.RWMutex
	path            string
	prefix          string
	cleanupInterval time.Duration
	shardDirs       sync.Map // pre-created shard dirs so per-write MkdirAll is avoided
	done            chan struct{}
	closeOnce       sync.Once

	// maxValueBytes caps the serialized size of a single value accepted by
	// Put/Add/Forever; 0 means unlimited. Immutable after construction.
	maxValueBytes int64

	// walkHook, when non-nil, is invoked once per file visited by the
	// lock-free walks in sweepExpired and FlushCtx. Test instrumentation
	// only: lets tests prove the store mutex is not held during the walk.
	walkHook func()

	// swapMatchedHook, when non-nil, is invoked by CompareAndSwapCtx after
	// the stored value matched and before the replacement is written, with
	// the key's write lock and the store mutex held. Test instrumentation
	// only: lets tests pause a swap between its comparison and its write.
	swapMatchedHook func()

	// lockStepHook, when non-nil, is invoked with "guard-opened" after a
	// write lock's file is opened and before it is flocked, and with
	// "record-read" by FileLock.GetWithErr after it read the lock record
	// and before it writes its own, under the key's guard. Test
	// instrumentation only: lets tests pause a lock acquire between steps.
	lockStepHook func(step string)

	// expiredRemoveHook, when non-nil, is invoked by a read or the expiry
	// sweep after it found an entry still expired under the key's write
	// lock and the store mutex, and before it removes the file. Test
	// instrumentation only: lets tests race a write against the removal.
	expiredRemoveHook func()

	// lockStore and friends back FileStore.Lock (see FileLock) so the
	// file driver satisfies the Locker capability. Created lazily on
	// first Lock call; lockErr captures any initialisation failure so
	// subsequent calls don't repeatedly attempt MkdirAll on a broken
	// filesystem.
	lockOnce  sync.Once
	lockStore *fileLockStore
	lockErr   error
}

// fileCacheItem represents a cached item stored in file.
//
// Value holds the bytes produced by MarshalValue, which for invalid-UTF-8
// strings is a raw 0x00-framed payload rather than valid JSON. It is therefore
// typed []byte (encoded as base64 in the item JSON) instead of json.RawMessage,
// which would reject the non-JSON framed bytes on marshal.
//
// A string set (SetAddCtx) is stored in Members, sorted, with Value empty.
// No other write stores an empty Value (MarshalValue of any value yields
// at least one byte), so a read tells a set from a value by Members alone.
// Each member is kept as its bytes (base64 in the item JSON), not as a
// JSON string: a JSON string would replace invalid UTF-8 with U+FFFD, so a
// member would read back different from what was added, removal by the
// original bytes would miss, and distinct members could collide.
type fileCacheItem struct {
	Value      []byte     `json:"value"`
	Expiration *time.Time `json:"expiration,omitempty"`
	Members    [][]byte   `json:"members,omitempty"`
}

// isSet reports whether the item holds a string set.
func (item fileCacheItem) isSet() bool {
	return len(item.Value) == 0 && len(item.Members) > 0
}

// live reports whether the item has not expired.
func (item fileCacheItem) live() bool {
	return item.Expiration == nil || time.Now().Before(*item.Expiration)
}

// expired reports whether the item's expiration has passed: what a read
// treats as a miss and the sweep removes.
func (item fileCacheItem) expired() bool {
	return item.Expiration != nil && time.Now().After(*item.Expiration)
}

// value returns what a read of the item returns: a set reads back as a
// map[string]struct{} of its members (the form the memory driver stores),
// anything else as UnmarshalValue decodes it.
func (item fileCacheItem) value() (interface{}, error) {
	if item.isSet() {
		set := make(map[string]struct{}, len(item.Members))
		for _, m := range item.Members {
			set[string(m)] = struct{}{}
		}
		return set, nil
	}
	return UnmarshalValue(item.Value)
}

// FileOption configures a FileStore at construction time.
type FileOption func(*FileStore)

// WithFileMaxValueBytes caps the serialized size of a single cached value.
// n > 0 rejects oversized Put/Add/Forever with ErrValueTooLarge; n <= 0
// (the default) means unlimited, preserving historical behaviour. The cap
// is per-value; aggregate growth is bounded separately (entry caps,
// expired-file cleanup).
func WithFileMaxValueBytes(n int64) FileOption {
	return func(s *FileStore) {
		if n > 0 {
			s.maxValueBytes = n
		} else {
			s.maxValueBytes = 0
		}
	}
}

// NewFileStore creates a new file cache store.
// The cache root directory is created up-front so individual Put calls don't
// have to MkdirAll on every write. Call Start() to begin the background
// expired-item cleanup goroutine.
func NewFileStore(prefix, path string, opts ...FileOption) (*FileStore, error) {
	return NewFileStoreWithOptions(prefix, path, DefaultFileCleanupInterval, opts...)
}

// NewFileStoreWithOptions creates a new file cache store with a configurable
// cleanup interval. Pass 0 to use DefaultFileCleanupInterval. This exists so
// tests (and callers that want to tune memory/disk tradeoffs) don't have to
// wait for the 5-minute default.
func NewFileStoreWithOptions(prefix, path string, cleanupInterval time.Duration, opts ...FileOption) (*FileStore, error) {
	if path == "" {
		path = "storage/framework/cache/data"
	}

	// Create cache root up-front. Callers that add new shard directories
	// later (see getCacheFilePath) also MkdirAll, but the common case is
	// covered here and avoids the system call on every Put.
	if err := os.MkdirAll(path, cacheDirMode); err != nil {
		return nil, fmt.Errorf("velocity/cache: failed to create cache directory: %w", err)
	}

	if cleanupInterval <= 0 {
		cleanupInterval = DefaultFileCleanupInterval
	}

	s := &FileStore{
		path:            path,
		prefix:          prefix,
		cleanupInterval: cleanupInterval,
		done:            make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// MaxValueBytes reports the store's per-value size cap; 0 means unlimited.
func (s *FileStore) MaxValueBytes() int64 {
	return s.maxValueBytes
}

// checkValueSize enforces the optional per-value cap against the serialized
// value bytes. Returns nil when no cap is configured.
func (s *FileStore) checkValueSize(valueData []byte) error {
	if s.maxValueBytes > 0 && int64(len(valueData)) > s.maxValueBytes {
		return fmt.Errorf("velocity/cache: value size %d exceeds maximum of %d bytes: %w", len(valueData), s.maxValueBytes, ErrValueTooLarge)
	}
	return nil
}

// Start begins the background goroutine that periodically removes expired
// cache files. Must be called after construction. Wrapped with async.Go so
// any panic in the walker is recovered instead of tearing down the process.
func (s *FileStore) Start() {
	async.Go(func() { s.cleanupExpired() })
}

// Shutdown stops the background cleanup goroutine. Safe to call multiple
// times. Honours the context deadline for uniformity with other
// ShutdownAware types.
func (s *FileStore) Shutdown(ctx context.Context) error {
	s.closeOnce.Do(func() {
		close(s.done)
	})
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

// cleanupExpired removes expired cache files periodically.
// It stops when the done channel is closed via Shutdown().
func (s *FileStore) cleanupExpired() {
	ticker := time.NewTicker(s.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.sweepExpired()
		}
	}
}

// sweepExpired performs one cleanup pass. The full-tree walk -- including
// the per-file read and JSON decode -- runs WITHOUT the store mutex, so a
// large cache no longer stalls every Get/Put for the duration of the sweep.
// The walk only collects candidate paths; each candidate is then re-verified
// and removed under a briefly-held lock (see removeIfEligible), which closes
// the race where a concurrent Put refreshes an entry between the lock-free
// observation and the removal.
func (s *FileStore) sweepExpired() {
	var candidates []string
	keyLocks := s.keyLockDir()
	filepath.Walk(s.path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if path == keyLocks {
				// The key write-lock files are not cache entries and
				// must never be removed (see lockKeyForWrite).
				return filepath.SkipDir
			}
			return nil
		}
		if s.walkHook != nil {
			s.walkHook()
		}

		// A write's temp file is not an entry: an in-flight write until
		// the grace has passed, a crashed one's leftover after it.
		if isTempFile(path) {
			if time.Since(info.ModTime()) > fileUnreadableGrace {
				candidates = append(candidates, path)
			}
			return nil
		}

		// Read file to check expiration
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		var item fileCacheItem
		if err := json.Unmarshal(data, &item); err != nil {
			// Unreadable: corrupt, a legacy on-disk schema from before
			// the value-encoding change, or an in-flight AddCtx create
			// by another instance/process sharing this path on a
			// filesystem without hard links (an O_EXCL zero-byte file
			// before its payload lands; see createFileExclusive). GetCtx
			// can never serve it, so purge it to avoid leaking the
			// file forever -- but only once it is older than the grace
			// window, so a live concurrent write is never deleted.
			if time.Since(info.ModTime()) > fileUnreadableGrace {
				candidates = append(candidates, path)
			}
			return nil
		}

		// Collect if expired
		if item.Expiration != nil && time.Now().After(*item.Expiration) {
			candidates = append(candidates, path)
		}

		return nil
	})

	for _, path := range candidates {
		s.removeIfEligible(path)
	}
}

// removeIfEligible re-reads a cleanup candidate under its key's write
// lock and the store mutex, the order every write takes them in, and
// deletes it only if it is still expired (or still unreadable past the
// grace window). A write of the key from any FileStore sharing the
// directory, in this process or another, that refreshed the entry after
// the lock-free walk observed it either completed before the lock was
// taken (the re-check sees the fresh entry and skips) or waits behind the
// removal (its write lands after it, exactly as if it had raced a Forget).
//
// The key's write lock is taken without waiting (see tryLockFileStripe):
// when another holder has it the file is left for a later sweep, so a
// sweep never blocks on a busy key. A temp file is likewise removed only
// under its stripe's lock: a held stripe may be a live writer that was
// paused past the grace, whose rename would fail on a removed file. Where
// flock is missing the removal runs under the store mutex alone, as every
// write does there.
func (s *FileStore) removeIfEligible(path string) {
	unlock, ok := s.tryLockFileStripe(path)
	if !ok {
		return
	}
	defer unlock()
	if isTempFile(path) {
		_ = s.removeTempIfStale(path)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return // already gone (or unreadable at the FS level); nothing to do
	}

	var item fileCacheItem
	if err := json.Unmarshal(data, &item); err != nil {
		if info, serr := os.Stat(path); serr == nil && time.Since(info.ModTime()) > fileUnreadableGrace {
			os.Remove(path)
		}
		return
	}

	if item.expired() {
		s.removeExpiredLocked(path)
	}
}

// removeExpiredLocked removes the expired entry at path. The caller holds
// the key's write lock (where flock exists) and the store mutex, and has
// just re-read the entry as expired.
func (s *FileStore) removeExpiredLocked(path string) {
	if s.expiredRemoveHook != nil {
		s.expiredRemoveHook()
	}
	_ = os.Remove(path)
}

// tryLockFileStripe takes, without waiting, the write lock of the stripe a
// file under the cache directory belongs to: an entry or a write's temp
// file (see entryStripe). Every write holds its key's lock from its read
// to its rename, so under the lock an entry does not change and a temp
// file whose stripe is free is a crashed write's leftover, while one whose
// stripe is held may be a live write paused past the grace. ok is false
// when the stripe is held or its lock cannot be taken; cleanup then leaves
// the file for a later pass. Where flock is missing (and for a file
// outside a shard directory, such as a Lock() record) ok is true with
// nothing locked: cleanup runs under the store mutex alone, as before.
func (s *FileStore) tryLockFileStripe(path string) (unlock func(), ok bool) {
	stripe, known := s.entryStripe(path)
	if !known {
		return func() {}, true
	}
	unlock, busy, err := s.flockStripe(stripe)
	if errors.Is(err, ErrLockNotSupported) {
		return func() {}, true
	}
	if err != nil || busy {
		return nil, false
	}
	return unlock, true
}

// removeTempIfStale removes a write's temp file when it is still older
// than the grace. The caller holds its stripe (tryLockFileStripe or the
// Flush group lock); the store mutex is taken here, stripe before mutex.
func (s *FileStore) removeTempIfStale(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) <= fileUnreadableGrace {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// getCacheFilePath returns the file path for a cache key.
// A 2-char sharded directory is created lazily on first use and cached in
// shardDirs so subsequent writes to the same shard skip the MkdirAll syscall.
func (s *FileStore) getCacheFilePath(key string) string {
	hasher := sha256.New()
	hasher.Write([]byte(s.prefixedKey(key)))
	hash := hex.EncodeToString(hasher.Sum(nil))

	shard := hash[:2]
	dir := filepath.Join(s.path, shard)

	if _, seen := s.shardDirs.Load(shard); !seen {
		// Ignore error: subsequent writes will surface it if the dir is
		// unusable. Cache the shard regardless to avoid repeated syscalls.
		_ = os.MkdirAll(dir, cacheDirMode)
		s.shardDirs.Store(shard, struct{}{})
	}

	return filepath.Join(dir, hash)
}

// prefixedKey returns the key with prefix.
func (s *FileStore) prefixedKey(key string) string {
	return PrefixKey(s.prefix, key)
}

// keyLockDir is the directory of the key write-lock files.
func (s *FileStore) keyLockDir() string {
	return filepath.Join(s.path, "locks", "keys")
}

// keyStripe names the write-lock stripe of key: the first byte of the
// hash of the prefixed key, in hex. It is also the name of the shard
// directory holding the key's entry and the first two characters of the
// entry's file name, so the stripe of a file found on disk is known
// without the key (see entryStripe).
func (s *FileStore) keyStripe(key string) string {
	sum := sha256.Sum256([]byte(s.prefixedKey(key)))
	return hex.EncodeToString(sum[:1])
}

// entryStripe returns the write-lock stripe of a file under the cache
// directory: an entry (<shard>/<hash>) or a write's temp file beside one
// (<shard>/<hash>.tmp-*). ok is false for any other file, such as the
// Lock() files under locks/.
func (s *FileStore) entryStripe(path string) (stripe string, ok bool) {
	dir := filepath.Dir(path)
	if filepath.Dir(dir) != filepath.Clean(s.path) {
		return "", false
	}
	name := filepath.Base(path)
	if i := strings.Index(name, fileTempMarker); i >= 0 {
		name = name[:i]
	}
	if len(name) != 2*sha256.Size {
		return "", false
	}
	if _, err := hex.DecodeString(name); err != nil {
		return "", false
	}
	if filepath.Base(dir) != name[:2] {
		return "", false
	}
	return name[:2], true
}

// lockKeyForPlainWrite takes key's write lock for a write whose contract
// holds within one process without it (Put, Add, Forever, Forget,
// Increment): where the platform has no flock the write proceeds under the
// store mutex alone, as it always has. Taking the lock is what makes a
// CompareAndSwapCtx or a set update from another process atomic against
// these writes.
func (s *FileStore) lockKeyForPlainWrite(ctx context.Context, key string) (func(), error) {
	unlock, err := s.lockKeyForWrite(ctx, key)
	if errors.Is(err, ErrLockNotSupported) {
		return func() {}, nil
	}
	return unlock, err
}

// GetCtx retrieves a value from the cache. The file store performs only
// local disk I/O that is not cancellable through context, so ctx is
// honoured as a pre-flight cancellation check but otherwise unused.
//
// A read of an expired entry is a miss and removes the entry (see
// removeExpired); a read of a live entry takes no write lock.
func (s *FileStore) GetCtx(ctx context.Context, key string) (interface{}, bool) {
	if ctx != nil && ctx.Err() != nil {
		return nil, false
	}
	s.mu.RLock()
	path := s.getCacheFilePath(key)
	data, err := os.ReadFile(path)
	s.mu.RUnlock()
	if err != nil {
		return nil, false
	}

	var item fileCacheItem
	if err := json.Unmarshal(data, &item); err != nil {
		return nil, false
	}

	if item.expired() {
		s.removeExpired(key, path)
		return nil, false
	}

	// Decode the value (a set reads back as its member map).
	value, err := item.value()
	if err != nil {
		return nil, false
	}

	return value, true
}

// removeExpired removes key's entry at path, which a read found expired,
// if it is still expired under the key's write lock and the store mutex,
// the order every write takes them in: a fresh write of the key from any
// FileStore sharing the directory that landed after the read is kept, and
// one still in flight lands after the removal. The key's write lock is
// taken without waiting, so a read never blocks on a writer; when another
// holder has it the entry is left for a later read or sweep. Where flock
// is missing the removal runs under the store mutex alone, as every write
// does there.
func (s *FileStore) removeExpired(key, path string) {
	unlock, busy, err := s.flockStripe(s.keyStripe(key))
	if errors.Is(err, ErrLockNotSupported) {
		unlock, busy, err = func() {}, false, nil
	}
	if err != nil || busy {
		return
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var item fileCacheItem
	if json.Unmarshal(data, &item) == nil && item.expired() {
		s.removeExpiredLocked(path)
	}
}

// Get retrieves a value from the cache.
//
// Deprecated: use GetCtx with a request-scoped context.Context.
func (s *FileStore) Get(key string) (interface{}, bool) {
	return s.GetCtx(context.Background(), key)
}

// GetStringCtx retrieves a string value from the cache.
func (s *FileStore) GetStringCtx(ctx context.Context, key string) (string, bool) {
	val, found := s.GetCtx(ctx, key)
	if !found {
		return "", false
	}
	str, ok := val.(string)
	if !ok {
		return "", false
	}
	return str, true
}

// GetString retrieves a string value from the cache.
//
// Deprecated: use GetStringCtx with a request-scoped context.Context.
func (s *FileStore) GetString(key string) (string, bool) {
	return s.GetStringCtx(context.Background(), key)
}

// PutCtx stores a value in the cache with a TTL.
func (s *FileStore) PutCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	unlock, err := s.lockKeyForPlainWrite(ctx, key)
	if err != nil {
		return err
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	// Marshal the value
	valueData, err := MarshalValue(value)
	if err != nil {
		return fmt.Errorf("velocity/cache: failed to marshal value: %w", err)
	}
	if err := s.checkValueSize(valueData); err != nil {
		return err
	}

	// ttl <= 0 means store forever (nil expiration), matching ForeverCtx;
	// computing time.Now().Add(ttl) unconditionally would persist an
	// already-expired entry for ttl=0.
	item := fileCacheItem{
		Value:      valueData,
		Expiration: expirationFor(ttl),
	}

	// Marshal the cache item
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("velocity/cache: failed to marshal cache item: %w", err)
	}

	// Write to file
	path := s.getCacheFilePath(key)
	if err := replaceFile(path, data); err != nil {
		return err
	}

	return nil
}

// Put stores a value in the cache with a TTL.
//
// Deprecated: use PutCtx with a request-scoped context.Context.
func (s *FileStore) Put(key string, value interface{}, ttl time.Duration) error {
	return s.PutCtx(context.Background(), key, value, ttl)
}

// Add atomically stores a value only if the key does not already
// exist (or its existing entry is expired). Returns true if inserted,
// false if a non-expired entry was already present or if another
// process holds the takeover lock for the same key.
//
// A non-nil error indicates a real backend failure (write or lock
// acquisition); callers must not treat (false, err) as benign
// contention.
//
// Atomicity is layered:
//
//   - Every write of the key, from any FileStore over the same directory,
//     holds the key's write lock (lockKeyForWrite) where flock exists.
//     AddCtx holds it from its first read of the entry to its write, so
//     the expired-entry takeover has exactly one winner across processes.
//   - Same-process goroutines serialize on the FileStore write mutex.
//   - The create path is gated by a hard link of the fully written temp
//     file onto the entry name (or os.O_EXCL where links are
//     unsupported): a kernel-enforced single creator on every platform,
//     see createFileExclusive.
//
// On a platform where flock is unavailable (the windows build), the
// expired-entry takeover path returns ErrLockNotSupported instead of
// degrading to last-writer-wins. There is no safe non-flock fallback
// that preserves the SETNX contract; operators that need cross-process
// single-flight on Windows should use the Redis driver. The fresh-key
// create path is still link/O_EXCL-protected on every platform.
func (s *FileStore) AddCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return false, err
		}
	}
	unlock, err := s.lockKeyForWrite(ctx, key)
	keyLocked := err == nil
	if errors.Is(err, ErrLockNotSupported) {
		unlock, err = func() {}, nil
	}
	if err != nil {
		return false, err
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.getCacheFilePath(key)
	valueData, err := MarshalValue(value)
	if err != nil {
		return false, fmt.Errorf("velocity/cache: failed to marshal value: %w", err)
	}
	if err := s.checkValueSize(valueData); err != nil {
		return false, err
	}
	// ttl <= 0 means store forever (nil expiration); an Add with ttl=0 must
	// insert a retrievable entry, not an already-expired one.
	item := fileCacheItem{
		Value:      valueData,
		Expiration: expirationFor(ttl),
	}
	data, err := json.Marshal(item)
	if err != nil {
		return false, fmt.Errorf("velocity/cache: failed to marshal cache item: %w", err)
	}

	// Atomic create-if-absent: the kernel refuses the hard link (or the
	// O_EXCL create on a filesystem without links) when the entry
	// exists, so exactly one creator wins the fresh-key path.
	if created, err := createFileExclusive(path, data); err != nil {
		return false, err
	} else if created {
		return true, nil
	}

	// File exists. Read it under the key's write lock and the process
	// mutex; if still valid, refuse insertion. If expired, take it over:
	// every writer of the key from any FileStore over the directory is
	// excluded until this write lands, so no other caller can have
	// taken it over in between.
	if existing, rerr := os.ReadFile(path); rerr == nil {
		// A zero-byte file means another instance just won the O_EXCL
		// create (the fallback of createFileExclusive on a filesystem
		// without hard links) and has not flushed its payload yet. The kernel
		// has already elected that creator the SETNX winner; we must
		// refuse insertion here. Falling through would race the
		// takeover path against a live creator and let both callers
		// return true (cache/drivers#TestFileStore_Add_CrossInstanceMutualExclusion).
		if len(existing) == 0 {
			return false, nil
		}
		var ex fileCacheItem
		if json.Unmarshal(existing, &ex) == nil {
			if ex.Expiration == nil || time.Now().Before(*ex.Expiration) {
				return false, nil
			}
		}
	}

	// Expired or unparseable entry. On platforms where flock(2) is
	// unavailable (windows) no key write lock is held, so there is no
	// safe way to honor the Store.Add SETNX contract for the takeover
	// path - last-writer-wins would let two processes both report
	// successful Add for the same expired key. Surface
	// ErrLockNotSupported instead so the caller knows the driver
	// cannot fulfil the contract on this platform; operators relying
	// on cross-process single-flight should use Redis or run on POSIX.
	if !keyLocked {
		return false, fmt.Errorf("velocity/cache: FileStore.Add cannot fulfil SETNX contract on this platform: %w", ErrLockNotSupported)
	}

	if werr := replaceFile(path, data); werr != nil {
		return false, werr
	}
	return true, nil
}

// Add atomically stores a value only if the key does not already exist.
//
// Deprecated: use AddCtx with a request-scoped context.Context.
func (s *FileStore) Add(key string, value interface{}, ttl time.Duration) (bool, error) {
	return s.AddCtx(context.Background(), key, value, ttl)
}

// ForeverCtx stores a value in the cache indefinitely.
func (s *FileStore) ForeverCtx(ctx context.Context, key string, value interface{}) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	unlock, err := s.lockKeyForPlainWrite(ctx, key)
	if err != nil {
		return err
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	// Marshal the value
	valueData, err := MarshalValue(value)
	if err != nil {
		return fmt.Errorf("velocity/cache: failed to marshal value: %w", err)
	}
	if err := s.checkValueSize(valueData); err != nil {
		return err
	}

	item := fileCacheItem{
		Value:      valueData,
		Expiration: nil,
	}

	// Marshal the cache item
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("velocity/cache: failed to marshal cache item: %w", err)
	}

	// Write to file
	path := s.getCacheFilePath(key)
	if err := replaceFile(path, data); err != nil {
		return err
	}

	return nil
}

// Forever stores a value in the cache indefinitely.
//
// Deprecated: use ForeverCtx with a request-scoped context.Context.
func (s *FileStore) Forever(key string, value interface{}) error {
	return s.ForeverCtx(context.Background(), key, value)
}

// ForgetCtx removes a value from the cache.
func (s *FileStore) ForgetCtx(ctx context.Context, key string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	unlock, err := s.lockKeyForPlainWrite(ctx, key)
	if err != nil {
		return err
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.getCacheFilePath(key)
	err = os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Forget removes a value from the cache.
//
// Deprecated: use ForgetCtx with a request-scoped context.Context.
func (s *FileStore) Forget(key string) error {
	return s.ForgetCtx(context.Background(), key)
}

// FlushCtx removes all values from the cache.
//
// The directory walk runs WITHOUT the store mutex so a flush over a large
// cache does not stall concurrent Get/Put for the whole traversal. A write
// that races the walk (lands in a shard the walk already passed) survives
// the flush -- observationally identical to the same Put issued just after
// Flush returned, which the previous whole-walk lock allowed too.
//
// Each entry is removed under its key's write lock (the stripe named by
// its file name, see entryStripe) and then the store mutex, the order
// every write takes them in. A write of the key from any FileStore sharing
// the directory therefore lands wholly before the removal or wholly after
// it: in particular a CompareAndSwapCtx that has matched the stored value
// finishes its write before the entry is removed, and one that has not
// yet read it finds the key gone, so a swap never recreates a key a
// returned Flush deleted. A held stripe is waited for, honouring ctx, as a
// write waits for it.
func (s *FileStore) FlushCtx(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	// Collect every file path lock-free. The key write-lock files are not
	// cache entries and are never removed (see lockKeyForWrite).
	var paths []string
	keyLocks := s.keyLockDir()
	err := filepath.Walk(s.path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Entries can vanish mid-walk now that concurrent ops (expired
			// Get removal, Forget) proceed during the traversal.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() && path == keyLocks {
			return filepath.SkipDir
		}
		if !info.IsDir() {
			if s.walkHook != nil {
				s.walkHook()
			}
			// A write's temp file is an in-flight write, not an entry:
			// removing it would fail that write's rename. Only a crashed
			// write's leftover goes: older than the grace, and with its
			// stripe free (see flushStripe).
			if isTempFile(path) && time.Since(info.ModTime()) <= fileUnreadableGrace {
				return nil
			}
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Group the entries and old temp files by stripe so each stripe's lock
	// is taken once. A file that belongs to no stripe (the Lock() files
	// under locks/) is removed under the store mutex alone.
	byStripe := make(map[string]*flushGroup)
	var stripes, loose []string
	for _, path := range paths {
		stripe, ok := s.entryStripe(path)
		if !ok {
			loose = append(loose, path)
			continue
		}
		g := byStripe[stripe]
		if g == nil {
			g = &flushGroup{}
			byStripe[stripe] = g
			stripes = append(stripes, stripe)
		}
		if isTempFile(path) {
			g.temps = append(g.temps, path)
		} else {
			g.entries = append(g.entries, path)
		}
	}
	for _, stripe := range stripes {
		if err := s.flushStripe(ctx, stripe, byStripe[stripe]); err != nil {
			return err
		}
	}
	for _, path := range loose {
		if err := s.removeLocked(path); err != nil {
			return err
		}
	}
	return nil
}

// flushGroup is what Flush removes under one stripe's write lock.
type flushGroup struct {
	entries []string
	temps   []string // temp files older than the grace
}

// flushStripe removes the entries and old temp files of one stripe under
// its write lock. With entries to remove it waits for the lock; with only
// temp files it takes the lock without waiting and leaves them when it is
// held, since a holder may be the live writer of one of them (see
// tryLockFileStripe). A temp file found under the lock is re-checked for
// age before it goes. Where flock is missing the files are removed under
// the store mutex alone.
func (s *FileStore) flushStripe(ctx context.Context, stripe string, g *flushGroup) error {
	var unlock func()
	if len(g.entries) > 0 {
		var err error
		unlock, err = s.lockStripe(ctx, stripe, "stripe "+stripe)
		if errors.Is(err, ErrLockNotSupported) {
			unlock, err = func() {}, nil
		}
		if err != nil {
			return fmt.Errorf("velocity/cache: FileStore.Flush: %w", err)
		}
	} else {
		var ok bool
		if unlock, ok = s.tryLockFileStripe(g.temps[0]); !ok {
			return nil
		}
	}
	defer unlock()
	for _, path := range g.temps {
		if err := s.removeTempIfStale(path); err != nil {
			return err
		}
	}
	for _, path := range g.entries {
		if err := s.removeLocked(path); err != nil {
			return err
		}
	}
	return nil
}

// removeLocked removes path under the store mutex. Holding the mutex for
// each removal keeps Flush excluded from a same-process AddCtx between its
// O_EXCL create and payload write (the fallback without hard links), so a
// successful Add is never silently emptied by a concurrent Flush.
func (s *FileStore) removeLocked(path string) error {
	s.mu.Lock()
	rmErr := os.Remove(path)
	s.mu.Unlock()
	if rmErr != nil && !os.IsNotExist(rmErr) {
		return rmErr
	}
	return nil
}

// Flush removes all values from the cache.
//
// Deprecated: use FlushCtx with a request-scoped context.Context.
func (s *FileStore) Flush() error {
	return s.FlushCtx(context.Background())
}

// IncrementCtx increments a numeric value.
func (s *FileStore) IncrementCtx(ctx context.Context, key string, value int64) (int64, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	unlock, err := s.lockKeyForPlainWrite(ctx, key)
	if err != nil {
		return 0, err
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	var current int64
	var expiration *time.Time

	// Try to get current value. An existing-but-non-numeric value is an
	// error — silently coercing it to zero (the prior behavior) meant a
	// caller who accidentally Put a string and then Increment'd would see
	// the counter quietly reset, which is exactly the kind of silent
	// corruption integration parity tests are supposed to catch. Match
	// MemoryStore's error message so the parity test asserts one string
	// across drivers.
	path := s.getCacheFilePath(key)
	if data, err := os.ReadFile(path); err == nil {
		var item fileCacheItem
		if err := json.Unmarshal(data, &item); err == nil {
			// Check expiration — expired entries fall through with current=0,
			// same as nonexistent files. Both are legitimate "start from 0" paths.
			if item.Expiration == nil || time.Now().Before(*item.Expiration) {
				// Decode through the shared serializer so 0x00-framed binary
				// strings are recognised. A decode failure on an existing,
				// unexpired entry is surfaced as a non-numeric error rather
				// than silently resetting the counter to zero.
				val, derr := UnmarshalValue(item.Value)
				if derr != nil {
					return 0, fmt.Errorf("velocity/cache: value is not numeric")
				}
				switch v := val.(type) {
				case float64:
					current = int64(v)
				case int64:
					current = v
				case int:
					current = int64(v)
				default:
					return 0, fmt.Errorf("velocity/cache: value is not numeric")
				}
				expiration = item.Expiration
			}
		}
	}

	newValue := current + value

	// Marshal the new value
	valueData, err := json.Marshal(newValue)
	if err != nil {
		return 0, err
	}

	item := fileCacheItem{
		Value:      valueData,
		Expiration: expiration,
	}

	// Marshal and save
	data, err := json.Marshal(item)
	if err != nil {
		return 0, err
	}

	if err := replaceFile(path, data); err != nil {
		return 0, err
	}

	return newValue, nil
}

// Increment increments a numeric value.
//
// Deprecated: use IncrementCtx with a request-scoped context.Context.
func (s *FileStore) Increment(key string, value int64) (int64, error) {
	return s.IncrementCtx(context.Background(), key, value)
}

// DecrementCtx decrements a numeric value.
func (s *FileStore) DecrementCtx(ctx context.Context, key string, value int64) (int64, error) {
	return s.IncrementCtx(ctx, key, -value)
}

// Decrement decrements a numeric value.
//
// Deprecated: use DecrementCtx with a request-scoped context.Context.
func (s *FileStore) Decrement(key string, value int64) (int64, error) {
	return s.DecrementCtx(context.Background(), key, value)
}

// Remember gets from cache or computes and stores.
func (s *FileStore) Remember(key string, ttl time.Duration, callback func() interface{}) (interface{}, error) {
	return RememberFrom(s, s, key, ttl, callback)
}

// RememberForever gets from cache or computes and stores forever.
func (s *FileStore) RememberForever(key string, callback func() interface{}) (interface{}, error) {
	return RememberForeverFrom(s, s, key, callback)
}

// ManyCtx retrieves multiple values.
func (s *FileStore) ManyCtx(ctx context.Context, keys []string) map[string]interface{} {
	result := make(map[string]interface{})
	for _, key := range keys {
		if val, found := s.GetCtx(ctx, key); found {
			result[key] = val
		}
	}
	return result
}

// Many retrieves multiple values.
//
// Deprecated: use ManyCtx with a request-scoped context.Context.
func (s *FileStore) Many(keys []string) map[string]interface{} {
	return s.ManyCtx(context.Background(), keys)
}

// PutManyCtx stores multiple values.
func (s *FileStore) PutManyCtx(ctx context.Context, items map[string]interface{}, ttl time.Duration) error {
	for key, value := range items {
		if err := s.PutCtx(ctx, key, value, ttl); err != nil {
			return err
		}
	}
	return nil
}

// PutMany stores multiple values.
//
// Deprecated: use PutManyCtx with a request-scoped context.Context.
func (s *FileStore) PutMany(items map[string]interface{}, ttl time.Duration) error {
	return s.PutManyCtx(context.Background(), items, ttl)
}

// HasCtx checks if a key exists.
func (s *FileStore) HasCtx(ctx context.Context, key string) bool {
	_, found := s.GetCtx(ctx, key)
	return found
}

// Has checks if a key exists.
//
// Deprecated: use HasCtx with a request-scoped context.Context.
func (s *FileStore) Has(key string) bool {
	return s.HasCtx(context.Background(), key)
}

// GetPrefix returns the cache prefix
func (s *FileStore) GetPrefix() string {
	return s.prefix
}
