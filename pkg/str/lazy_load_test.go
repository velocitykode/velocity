package str

import (
	"fmt"
	"regexp"
	"testing"
)

func TestLazyLoading(t *testing.T) {
	t.Run("regex patterns are cached", func(t *testing.T) {
		// Clear cache for test isolation
		regexCache.Lock()
		regexCache.patterns = make(map[string]*regexp.Regexp)
		initialCount := len(regexCache.patterns)
		regexCache.Unlock()

		// First call should compile and cache the regex
		result1 := Slug("Hello World!", "-")
		if result1 != "hello-world" {
			t.Errorf("Slug failed: got %q", result1)
		}

		// Check cache has grown
		regexCache.RLock()
		afterFirstCall := len(regexCache.patterns)
		regexCache.RUnlock()

		if afterFirstCall <= initialCount {
			t.Error("Regex pattern was not cached after first use")
		}

		// Second call should use cached regex
		result2 := Slug("Another Test!", "-")
		if result2 != "another-test" {
			t.Errorf("Slug failed: got %q", result2)
		}

		// Cache size should be the same (reusing patterns)
		regexCache.RLock()
		afterSecondCall := len(regexCache.patterns)
		regexCache.RUnlock()

		if afterSecondCall != afterFirstCall {
			t.Errorf("Cache grew unexpectedly: before=%d, after=%d", afterFirstCall, afterSecondCall)
		}
	})

	t.Run("different patterns are cached separately", func(t *testing.T) {
		regexCache.Lock()
		regexCache.patterns = make(map[string]*regexp.Regexp)
		regexCache.Unlock()

		// Use InlineMarkdown which uses multiple regex patterns
		result := InlineMarkdown("**bold** and *italic* text")
		if result == "" {
			t.Error("InlineMarkdown failed")
		}

		regexCache.RLock()
		patternsAfterMarkdown := len(regexCache.patterns)
		regexCache.RUnlock()

		if patternsAfterMarkdown < 2 {
			t.Errorf("Expected multiple patterns cached, got %d", patternsAfterMarkdown)
		}
	})

	t.Run("concurrent access is safe", func(t *testing.T) {
		// This test verifies the mutex protection works
		done := make(chan bool, 10)

		for i := 0; i < 10; i++ {
			go func(id int) {
				// Each goroutine uses a function that requires regex
				_ = Slug(fmt.Sprintf("Test String %d", id), "-")
				_ = InlineMarkdown(fmt.Sprintf("**Test %d**", id))
				done <- true
			}(i)
		}

		// Wait for all goroutines
		for i := 0; i < 10; i++ {
			<-done
		}

		// If we get here without deadlock/panic, concurrent access is safe
		t.Log("Concurrent regex access completed successfully")
	})

	t.Run("patterns are only compiled once", func(t *testing.T) {
		regexCache.Lock()
		regexCache.patterns = make(map[string]*regexp.Regexp)
		regexCache.Unlock()

		// Call a function multiple times with same pattern needs
		for i := 0; i < 5; i++ {
			_ = Slug("Test String", "-")
		}

		regexCache.RLock()
		finalCount := len(regexCache.patterns)
		regexCache.RUnlock()

		// Should only have patterns needed for Slug, not 5x
		if finalCount > 2 {
			t.Errorf("Patterns were recompiled: expected <=2, got %d", finalCount)
		}
	})
}

func BenchmarkLazyLoadedRegex(b *testing.B) {
	// Benchmark to show cached regex is faster than compiling each time
	b.Run("cached regex", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = Slug("Hello World Test", "-")
		}
	})

	b.Run("inline markdown with cache", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = InlineMarkdown("**bold** *italic* `code` [link](url)")
		}
	})
}
