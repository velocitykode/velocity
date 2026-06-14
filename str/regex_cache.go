package str

import (
	"regexp"
	"sync"
	"sync/atomic"
)

// regexCacheMax bounds the number of cached compiled regexes. On overflow an
// arbitrary entry is evicted so an attacker who controls patterns (via
// Match/MatchAll/Test/Is) cannot drive unbounded memory growth. Framework-
// internal callers use a handful of patterns; this limit is well above that
// working set, so they never evict and always hit cache.
const regexCacheMax = 1024

// regexCache holds compiled patterns, bounded at regexCacheMax.
//
// Reads (cache hits) are lock-free: a sync.Map Load with no shared write, so
// concurrent hits scale across cores instead of serializing. The previous
// implementation took an exclusive mutex on every hit to move an LRU recency
// node; tracking recency is precisely what forces a shared write per read, so
// it is dropped here. The exclusive lock (evictMu) is taken only to evict on
// overflow -- a cold path that only an attacker flooding distinct patterns can
// reach, since the framework's own pattern set is tiny and permanent.
//
// This mirrors the lock-free read approach in cache/drivers/memory.go, minus
// the approximate-LRU recency stamp: for a fixed, hot, handful-sized working
// set, eviction quality is irrelevant (an evicted framework pattern simply
// recompiles once on its next use) but read scalability is everything.
type regexLRU struct {
	max     int
	entries sync.Map // pattern string -> *regexp.Regexp

	// count tracks the number of live entries so the bound can be enforced
	// without an O(n) scan of the map. It is kept in step with entries:
	// incremented on a genuine insert, decremented on eviction.
	count atomic.Int64

	// evictMu serializes eviction so concurrent overflowing inserts do not
	// trim past the cap. It is never held on the read path.
	evictMu sync.Mutex
}

func newRegexLRU(max int) *regexLRU {
	return &regexLRU{max: max}
}

// get returns the cached regex for pattern and reports whether it was found.
// Lock-free: a single sync.Map load with no shared write, so concurrent hits
// do not contend.
func (c *regexLRU) get(pattern string) (*regexp.Regexp, bool) {
	if v, ok := c.entries.Load(pattern); ok {
		return v.(*regexp.Regexp), true
	}
	return nil, false
}

// put stores pattern -> re and returns the canonical regex for pattern. If a
// concurrent compile of the same pattern already stored one, that existing
// regex is returned and re is discarded, so all callers converge on a single
// *regexp.Regexp (idempotent insert). A genuine insert that pushes the cache
// over capacity triggers eviction.
func (c *regexLRU) put(pattern string, re *regexp.Regexp) *regexp.Regexp {
	if actual, loaded := c.entries.LoadOrStore(pattern, re); loaded {
		return actual.(*regexp.Regexp)
	}
	if c.count.Add(1) > int64(c.max) {
		c.evict()
	}
	return re
}

// evict trims the cache back to the cap. sync.Map Range visits in randomized
// order, so this drops arbitrary entries rather than tracking per-hit recency
// (which would force a shared write on every read and serialize the hot path).
func (c *regexLRU) evict() {
	c.evictMu.Lock()
	defer c.evictMu.Unlock()
	// Re-check under the lock: a concurrent evictor may have already trimmed.
	for c.count.Load() > int64(c.max) {
		removed := false
		c.entries.Range(func(k, _ any) bool {
			if _, ok := c.entries.LoadAndDelete(k); ok {
				c.count.Add(-1)
				removed = true
			}
			return c.count.Load() > int64(c.max)
		})
		if !removed {
			break
		}
	}
}

// len returns the current cache size. Used by tests.
func (c *regexLRU) len() int {
	return int(c.count.Load())
}

// clear empties the cache. Used by tests for isolation.
func (c *regexLRU) clear() {
	c.evictMu.Lock()
	defer c.evictMu.Unlock()
	c.entries.Range(func(k, _ any) bool {
		c.entries.Delete(k)
		return true
	})
	c.count.Store(0)
}

// regexCache holds compiled patterns with a bounded, lock-free read path. The
// 1024-entry cap prevents unbounded growth when callers feed user-controlled
// patterns through Match/MatchAll/Test/Is. Framework-internal callers use far
// fewer than 1024 distinct patterns, so they always hit cache.
var regexCache = newRegexLRU(regexCacheMax)

// getRegexE returns a cached regex, compiling on miss. Returns an error if
// the pattern is malformed.
func getRegexE(pattern string) (*regexp.Regexp, error) {
	if re, ok := regexCache.get(pattern); ok {
		return re, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	return regexCache.put(pattern, re), nil
}

// getRegex returns a cached regex, compiling on miss. Panics if pattern is
// malformed. Use only with trusted, framework-internal patterns. For user-
// controlled patterns use getRegexE.
func getRegex(pattern string) *regexp.Regexp {
	re, err := getRegexE(pattern)
	if err != nil {
		panic(err)
	}
	return re
}

// mustMatch is a helper that uses cached regex for matching. Trusted
// patterns only.
func mustMatch(pattern, text string) bool {
	return getRegex(pattern).MatchString(text)
}

// mustReplace is a helper that uses cached regex for replacement. Trusted
// patterns only.
func mustReplace(pattern, text, replacement string) string {
	return getRegex(pattern).ReplaceAllString(text, replacement)
}
