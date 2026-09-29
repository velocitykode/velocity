package drivers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"time"
)

// readLiveItemLocked reads the item stored at path. ok is false when the
// file is absent, empty (an AddCtx create whose payload has not landed),
// unreadable or expired: every case a GetCtx reports as a miss. Caller
// holds s.mu.
func (s *FileStore) readLiveItemLocked(path string) (item fileCacheItem, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return fileCacheItem{}, false
	}
	if err := json.Unmarshal(data, &item); err != nil {
		return fileCacheItem{}, false
	}
	if !item.live() {
		return fileCacheItem{}, false
	}
	return item, true
}

// writeItemLocked replaces the item at path whole (replaceFile), as
// PutCtx does. Caller holds s.mu and the key's write lock.
func (s *FileStore) writeItemLocked(path string, item fileCacheItem) error {
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("velocity/cache: failed to marshal cache item: %w", err)
	}
	return replaceFile(path, data)
}

// CompareAndSwapCtx implements contract.CacheSwapper. The file store is a
// serializing store, so a read returns the decoded shape of the stored
// bytes and the swap compares with that equality: the stored value and
// expected are compared as a read produces them (MatchesStoredValue), so
// the unchanged value a read returned always matches, and two stored
// values no read can tell apart match each other. A stored set compares
// as the member map a read of it returns.
//
// The read, the comparison and the write run under the store mutex and
// the key's write lock (a flock shared by every FileStore over the same
// directory, see lockKeyForWrite), which every write of the key takes, so
// the swap is atomic against the exact stored value it matched, across
// instances and processes. A held lock is waited for; an absent, expired
// or different entry yields (false, nil) and nothing is written. On a
// platform without flock the swap returns an error wrapping
// ErrLockNotSupported, since the store mutex alone cannot exclude another
// process.
func (s *FileStore) CompareAndSwapCtx(ctx context.Context, key string, expected, value interface{}, ttl time.Duration) (bool, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return false, err
		}
	}
	valueData, err := MarshalValue(value)
	if err != nil {
		return false, fmt.Errorf("velocity/cache: failed to marshal value: %w", err)
	}
	if err := s.checkValueSize(valueData); err != nil {
		return false, err
	}
	unlock, err := s.lockKeyForWrite(ctx, key)
	if err != nil {
		return false, fmt.Errorf("velocity/cache: FileStore.CompareAndSwap: %w", err)
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.getCacheFilePath(key)
	item, ok := s.readLiveItemLocked(path)
	if !ok {
		return false, nil
	}
	if item.isSet() {
		current, _ := item.value()
		if !reflect.DeepEqual(current, expected) {
			return false, nil
		}
	} else {
		if _, err := UnmarshalValue(item.Value); err != nil {
			// A read reports this entry as a miss: nothing to match.
			return false, nil
		}
		same, err := MatchesStoredValue(item.Value, expected)
		if err != nil {
			return false, fmt.Errorf("velocity/cache: failed to compare expected value: %w", err)
		}
		if !same {
			return false, nil
		}
	}
	if s.swapMatchedHook != nil {
		s.swapMatchedHook() //lock-held-ok: swapMatchedHook is a test-only hook, nil outside tests
	}
	if err := s.writeItemLocked(path, fileCacheItem{Value: valueData, Expiration: expirationFor(ttl)}); err != nil {
		return false, err
	}
	return true, nil
}

// SetAddCtx implements contract.CacheSetStore. The set is stored under
// key as its sorted members (fileCacheItem.Members); a read of the key
// returns them as a map[string]struct{}, as the memory driver does. The
// update runs under the store mutex and the key's write lock, so adds and
// removes from every FileStore over the same directory, in any process,
// never lose each other's members. The expiry is extend-only: a live set
// keeps the later of its current deadline and now+ttl, a set without a
// deadline stays that way, and ttl <= 0 removes the deadline. A key
// holding a value other than a set is replaced by a new set. On a
// platform without flock it returns an error wrapping ErrLockNotSupported.
func (s *FileStore) SetAddCtx(ctx context.Context, key string, ttl time.Duration, members ...string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if len(members) == 0 {
		return nil
	}
	unlock, err := s.lockKeyForWrite(ctx, key)
	if err != nil {
		return fmt.Errorf("velocity/cache: FileStore.SetAdd: %w", err)
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.getCacheFilePath(key)
	expiration := expirationFor(ttl)
	set := make(map[string]struct{}, len(members))
	if item, ok := s.readLiveItemLocked(path); ok && item.isSet() {
		for _, m := range item.Members {
			set[string(m)] = struct{}{}
		}
		switch {
		case item.Expiration == nil:
			expiration = nil
		case expiration != nil && item.Expiration.After(*expiration):
			expiration = item.Expiration
		}
	}
	for _, m := range members {
		set[m] = struct{}{}
	}
	return s.writeItemLocked(path, fileCacheItem{Members: sortedMembers(set), Expiration: expiration})
}

// SetRemoveCtx implements contract.CacheSetStore under the same lock as
// SetAddCtx. The set keeps its expiry; removing the last member deletes
// the key. A key that is absent, expired or not a set is left alone.
func (s *FileStore) SetRemoveCtx(ctx context.Context, key string, members ...string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if len(members) == 0 {
		return nil
	}
	unlock, err := s.lockKeyForWrite(ctx, key)
	if err != nil {
		return fmt.Errorf("velocity/cache: FileStore.SetRemove: %w", err)
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.getCacheFilePath(key)
	item, ok := s.readLiveItemLocked(path)
	if !ok || !item.isSet() {
		return nil
	}
	set := make(map[string]struct{}, len(item.Members))
	for _, m := range item.Members {
		set[string(m)] = struct{}{}
	}
	for _, m := range members {
		delete(set, m)
	}
	if len(set) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("velocity/cache: failed to remove cache file: %w", err)
		}
		return nil
	}
	return s.writeItemLocked(path, fileCacheItem{Members: sortedMembers(set), Expiration: item.Expiration})
}

// SetMembersCtx implements contract.CacheSetStore. It reads under the
// key's write lock, so it never sees a set update from another process
// half written. Returns nil for a key that is absent, expired or not a
// set.
func (s *FileStore) SetMembersCtx(ctx context.Context, key string) ([]string, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	unlock, err := s.lockKeyForWrite(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("velocity/cache: FileStore.SetMembers: %w", err)
	}
	defer unlock()
	s.mu.RLock()
	defer s.mu.RUnlock()

	item, ok := s.readLiveItemLocked(s.getCacheFilePath(key))
	if !ok || !item.isSet() {
		return nil, nil
	}
	out := make([]string, len(item.Members))
	for i, m := range item.Members {
		out[i] = string(m)
	}
	return out, nil
}

// sortedMembers returns the members of set as their bytes in sorted
// order, so the stored form of a set does not depend on map iteration.
func sortedMembers(set map[string]struct{}) [][]byte {
	keys := make([]string, 0, len(set))
	for m := range set {
		keys = append(keys, m)
	}
	sort.Strings(keys)
	out := make([][]byte, len(keys))
	for i, m := range keys {
		out[i] = []byte(m)
	}
	return out
}
