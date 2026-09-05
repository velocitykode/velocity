package str

import (
	"fmt"
	"testing"
)

func TestLazyLoading(t *testing.T) {
	t.Run("regex patterns are cached", func(t *testing.T) {
		// Clear cache for test isolation
		regexCache.clear()
		initialCount := regexCache.len()

		// First call should compile and cache the regex
		result1 := Slug("Hello World!", "-")
		if result1 != "hello-world" {
			t.Errorf("Slug failed: got %q", result1)
		}

		afterFirstCall := regexCache.len()
		if afterFirstCall <= initialCount {
			t.Error("Regex pattern was not cached after first use")
		}

		// Second call should use cached regex
		result2 := Slug("Another Test!", "-")
		if result2 != "another-test" {
			t.Errorf("Slug failed: got %q", result2)
		}

		afterSecondCall := regexCache.len()
		if afterSecondCall != afterFirstCall {
			t.Errorf("Cache grew unexpectedly: before=%d, after=%d", afterFirstCall, afterSecondCall)
		}
	})

	t.Run("different patterns are cached separately", func(t *testing.T) {
		regexCache.clear()

		// Each separator compiles its own collapse pattern on top of the
		// shared one, so two separators leave three patterns behind.
		if got := Slug("Hello World", "-"); got != "hello-world" {
			t.Errorf("Slug failed: got %q", got)
		}
		if got := Slug("Hello World", "_"); got != "hello_world" {
			t.Errorf("Slug failed: got %q", got)
		}

		if patterns := regexCache.len(); patterns < 3 {
			t.Errorf("Expected multiple patterns cached, got %d", patterns)
		}
	})

	t.Run("concurrent access is safe", func(t *testing.T) {
		// This test verifies the mutex protection works
		done := make(chan bool, 10)

		for i := 0; i < 10; i++ {
			go func(id int) {
				// Each goroutine uses a function that requires regex
				_ = Slug(fmt.Sprintf("Test String %d", id), "-")
				_ = Slug(fmt.Sprintf("Test String %d", id), "_")
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
		regexCache.clear()

		// Call a function multiple times with same pattern needs
		for i := 0; i < 5; i++ {
			_ = Slug("Test String", "-")
		}

		// Should only have patterns needed for Slug, not 5x
		if finalCount := regexCache.len(); finalCount > 2 {
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
}
