package events

import (
	"strings"
	"sync"
)

// WildcardListener wraps a listener for wildcard pattern matching
type WildcardListener struct {
	Pattern  string
	Listener Listener
}

// Matches checks if an event name matches the wildcard pattern
func (w *WildcardListener) Matches(eventName string) bool {
	return MatchesPattern(eventName, w.Pattern)
}

// MatchesPattern checks if an event name matches a wildcard pattern
func MatchesPattern(eventName, pattern string) bool {
	// Exact match
	if pattern == eventName {
		return true
	}

	// Match everything
	if pattern == "*" {
		return true
	}

	// Convert pattern to parts
	patternParts := strings.Split(pattern, ".")
	eventParts := strings.Split(eventName, ".")

	return matchParts(eventParts, patternParts)
}

// matchParts recursively matches event parts against pattern parts
func matchParts(eventParts, patternParts []string) bool {
	if len(patternParts) == 0 {
		return len(eventParts) == 0
	}

	if len(eventParts) == 0 {
		// Only match if remaining pattern is all wildcards
		for _, p := range patternParts {
			if p != "*" && p != "**" {
				return false
			}
		}
		return true
	}

	patternPart := patternParts[0]
	eventPart := eventParts[0]

	switch patternPart {
	case "*":
		// Single wildcard matches one part
		return matchParts(eventParts[1:], patternParts[1:])

	case "**":
		// Double wildcard matches zero or more parts
		if len(patternParts) == 1 {
			// ** at end matches everything
			return true
		}
		// Try matching with consuming 0, 1, 2... parts
		for i := 0; i <= len(eventParts); i++ {
			if matchParts(eventParts[i:], patternParts[1:]) {
				return true
			}
		}
		return false

	default:
		// Check for partial wildcards like "user*" or "*registered"
		if strings.Contains(patternPart, "*") {
			if matchPartialWildcard(eventPart, patternPart) {
				return matchParts(eventParts[1:], patternParts[1:])
			}
			return false
		}

		// Exact match required
		if patternPart != eventPart {
			return false
		}
		return matchParts(eventParts[1:], patternParts[1:])
	}
}

// matchPartialWildcard matches patterns like "user*" or "*registered"
func matchPartialWildcard(text, pattern string) bool {
	// Handle prefix wildcard (*text)
	if strings.HasPrefix(pattern, "*") {
		suffix := strings.TrimPrefix(pattern, "*")
		return strings.HasSuffix(text, suffix)
	}

	// Handle suffix wildcard (text*)
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(text, prefix)
	}

	// Handle middle wildcard (te*xt)
	parts := strings.Split(pattern, "*")
	if len(parts) == 2 {
		return strings.HasPrefix(text, parts[0]) && strings.HasSuffix(text, parts[1])
	}

	return false
}

// WildcardCache caches wildcard pattern matching results for performance
type WildcardCache struct {
	mu    sync.RWMutex
	cache map[string]map[string]bool // [pattern][event] = matches
}

// NewWildcardCache creates a new wildcard cache
func NewWildcardCache() *WildcardCache {
	return &WildcardCache{
		cache: make(map[string]map[string]bool),
	}
}

// Matches checks if an event matches a pattern using cache
func (c *WildcardCache) Matches(eventName, pattern string) bool {
	// Check cache first
	c.mu.RLock()
	if patternCache, ok := c.cache[pattern]; ok {
		if result, ok := patternCache[eventName]; ok {
			c.mu.RUnlock()
			return result
		}
	}
	c.mu.RUnlock()

	// Calculate match
	matches := MatchesPattern(eventName, pattern)

	// Store in cache
	c.mu.Lock()
	if _, ok := c.cache[pattern]; !ok {
		c.cache[pattern] = make(map[string]bool)
	}
	c.cache[pattern][eventName] = matches
	c.mu.Unlock()

	return matches
}

// Clear clears the cache
func (c *WildcardCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[string]map[string]bool)
}
