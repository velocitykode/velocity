package str

import (
	"container/list"
	"regexp"
	"sync"
)

// regexCacheMax bounds the number of cached compiled regexes. The cache uses
// LRU eviction so an attacker who controls patterns cannot drive unbounded
// memory growth. Framework-internal callers use a handful of patterns; this
// limit is well above that working set.
const regexCacheMax = 1024

// regexCacheEntry is a single LRU slot.
type regexCacheEntry struct {
	pattern string
	re      *regexp.Regexp
}

// regexLRU is a thread-safe LRU cache keyed by pattern string. Each Get on a
// hit moves the entry to the front. Put evicts the back entry when over
// capacity. Reads still take a write lock because LRU touches the recency
// list; this is a hot path only when patterns repeat, and compilation
// dominates cost on misses anyway.
type regexLRU struct {
	mu    sync.Mutex
	max   int
	ll    *list.List
	index map[string]*list.Element
}

func newRegexLRU(max int) *regexLRU {
	return &regexLRU{
		max:   max,
		ll:    list.New(),
		index: make(map[string]*list.Element, max),
	}
}

// get returns the cached regex for pattern and reports whether it was found.
// On a hit the entry is promoted to the front of the recency list.
func (c *regexLRU) get(pattern string) (*regexp.Regexp, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[pattern]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*regexCacheEntry).re, true
	}
	return nil, false
}

// put inserts pattern -> re. If the cache is over capacity after insert, the
// least-recently-used entry is evicted. If pattern is already present, its
// regex is updated (a rare path; useful only if a caller compiled a fresh
// regex while the entry was being evicted concurrently).
func (c *regexLRU) put(pattern string, re *regexp.Regexp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[pattern]; ok {
		el.Value.(*regexCacheEntry).re = re
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&regexCacheEntry{pattern: pattern, re: re})
	c.index[pattern] = el
	for c.ll.Len() > c.max {
		back := c.ll.Back()
		if back == nil {
			break
		}
		entry := back.Value.(*regexCacheEntry)
		delete(c.index, entry.pattern)
		c.ll.Remove(back)
	}
}

// len returns the current cache size. Used by tests.
func (c *regexLRU) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// clear empties the cache. Used by tests for isolation.
func (c *regexLRU) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll = list.New()
	c.index = make(map[string]*list.Element, c.max)
}

// regexCache holds compiled patterns with LRU eviction. The 1024-entry cap
// prevents unbounded growth when callers feed user-controlled patterns
// through Match/MatchAll/Test/Is. Framework-internal callers use far fewer
// than 1024 distinct patterns, so they always hit cache.
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
	regexCache.put(pattern, re)
	return re, nil
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

// mustFindAll is a helper that uses cached regex for finding all matches.
// Trusted patterns only.
func mustFindAll(pattern, text string) [][]string {
	return getRegex(pattern).FindAllStringSubmatch(text, -1)
}
