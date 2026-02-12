package log

import (
	"os"
)

// init is intentionally a no-op. Logger initialization is handled explicitly
// via NewLogger() or the global Get() fallback to console.
func init() {}

// getEnvOrDefault retrieves an environment variable or returns a default value if not set
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// LogConfig holds configuration for creating a Logger instance.
type LogConfig struct {
	// Driver specifies the log driver: "console" or "file".
	Driver string
	// Config holds driver-specific options (e.g., "path" for file driver).
	Config map[string]any
}

// LogConfigFromEnv builds a LogConfig from environment variables.
// It reads LOG_DRIVER, LOG_PATH, LOG_LEVEL, and LOG_FORMAT.
func LogConfigFromEnv() LogConfig {
	driver := os.Getenv("LOG_DRIVER")
	if driver == "" {
		driver = "console"
	}

	return LogConfig{
		Driver: driver,
		Config: map[string]any{
			"path":   getEnvOrDefault("LOG_PATH", "./storage/logs"),
			"level":  getEnvOrDefault("LOG_LEVEL", "debug"),
			"format": getEnvOrDefault("LOG_FORMAT", "text"),
		},
	}
}
