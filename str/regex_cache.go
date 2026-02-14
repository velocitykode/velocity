package str

import (
	"regexp"
	"sync"
)

// regexCache provides lazy-loaded, cached regex patterns
var regexCache = &struct {
	sync.RWMutex
	patterns map[string]*regexp.Regexp
}{
	patterns: make(map[string]*regexp.Regexp),
}

// getRegex returns a cached regex pattern, compiling it if necessary
func getRegex(pattern string) *regexp.Regexp {
	regexCache.RLock()
	if re, ok := regexCache.patterns[pattern]; ok {
		regexCache.RUnlock()
		return re
	}
	regexCache.RUnlock()

	regexCache.Lock()
	defer regexCache.Unlock()

	// Double-check after acquiring write lock
	if re, ok := regexCache.patterns[pattern]; ok {
		return re
	}

	re := regexp.MustCompile(pattern)
	regexCache.patterns[pattern] = re
	return re
}

// mustMatch is a helper that uses cached regex for matching
func mustMatch(pattern, text string) bool {
	return getRegex(pattern).MatchString(text)
}

// mustReplace is a helper that uses cached regex for replacement
func mustReplace(pattern, text, replacement string) string {
	return getRegex(pattern).ReplaceAllString(text, replacement)
}

// mustFindAll is a helper that uses cached regex for finding all matches
func mustFindAll(pattern, text string) [][]string {
	return getRegex(pattern).FindAllStringSubmatch(text, -1)
}
