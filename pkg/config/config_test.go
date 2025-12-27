package config

import (
	"os"
	"testing"
)

func TestGet(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		fallback string
		envValue string
		expected string
	}{
		{
			name:     "Returns environment variable when set",
			key:      "TEST_VAR_1",
			fallback: "default",
			envValue: "from_env",
			expected: "from_env",
		},
		{
			name:     "Returns fallback when env var not set",
			key:      "TEST_VAR_2",
			fallback: "default_value",
			envValue: "",
			expected: "default_value",
		},
		{
			name:     "Returns empty fallback when env var not set",
			key:      "TEST_VAR_3",
			fallback: "",
			envValue: "",
			expected: "",
		},
		{
			name:     "Returns environment variable when both are set",
			key:      "TEST_VAR_4",
			fallback: "fallback",
			envValue: "env_wins",
			expected: "env_wins",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean up before test
			os.Unsetenv(tt.key)

			// Set environment variable if provided
			if tt.envValue != "" {
				os.Setenv(tt.key, tt.envValue)
				defer os.Unsetenv(tt.key)
			}

			result := Get(tt.key, tt.fallback)
			if result != tt.expected {
				t.Errorf("Get(%q, %q) = %q; want %q", tt.key, tt.fallback, result, tt.expected)
			}
		})
	}
}

func TestGet_EmptyString(t *testing.T) {
	// Test that empty string env var returns fallback
	key := "TEST_EMPTY_VAR"
	os.Setenv(key, "")
	defer os.Unsetenv(key)

	result := Get(key, "fallback")
	if result != "fallback" {
		t.Errorf("Expected fallback for empty env var, got: %s", result)
	}
}

func TestGet_Whitespace(t *testing.T) {
	// Test that whitespace is preserved
	key := "TEST_WHITESPACE_VAR"
	os.Setenv(key, "  value with spaces  ")
	defer os.Unsetenv(key)

	result := Get(key, "fallback")
	if result != "  value with spaces  " {
		t.Errorf("Expected whitespace to be preserved, got: %q", result)
	}
}

func TestGet_ConcurrentAccess(t *testing.T) {
	// Test that concurrent access works correctly
	key := "TEST_CONCURRENT_VAR"
	os.Setenv(key, "concurrent_value")
	defer os.Unsetenv(key)

	done := make(chan bool)
	iterations := 100

	for i := 0; i < iterations; i++ {
		go func() {
			result := Get(key, "fallback")
			if result != "concurrent_value" {
				t.Errorf("Expected concurrent_value, got: %s", result)
			}
			done <- true
		}()
	}

	for i := 0; i < iterations; i++ {
		<-done
	}
}

func TestGet_MultipleKeys(t *testing.T) {
	// Test multiple keys with different values
	keys := map[string]string{
		"APP_NAME": "Velocity",
		"APP_ENV":  "production",
		"PORT":     "8080",
		"DEBUG":    "true",
	}

	// Set all env vars
	for key, value := range keys {
		os.Setenv(key, value)
		defer os.Unsetenv(key)
	}

	// Test all retrievals
	for key, expected := range keys {
		result := Get(key, "default")
		if result != expected {
			t.Errorf("Get(%q) = %q; want %q", key, result, expected)
		}
	}
}

func BenchmarkGet_WithEnv(b *testing.B) {
	key := "BENCH_VAR"
	os.Setenv(key, "benchmark_value")
	defer os.Unsetenv(key)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Get(key, "fallback")
	}
}

func BenchmarkGet_WithoutEnv(b *testing.B) {
	key := "NONEXISTENT_BENCH_VAR"
	os.Unsetenv(key)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Get(key, "fallback")
	}
}
