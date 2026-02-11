package log

import (
	"os"
	"sync"

	"github.com/joho/godotenv"
	"github.com/velocitykode/velocity/pkg/log/drivers"
)

var initOnce sync.Once

// init loads the .env file and auto-initializes the logger.
func init() {
	// Try to load .env file (optional - won't error if missing)
	godotenv.Load()

	// Auto-initialize with environment-based configuration
	initOnce.Do(func() {
		driver := os.Getenv("LOG_DRIVER")
		if driver == "" {
			driver = "console" // Default to console
		}

		config := map[string]any{
			"path":   getEnvOrDefault("LOG_PATH", "./storage/logs"),
			"level":  getEnvOrDefault("LOG_LEVEL", "debug"),
			"format": getEnvOrDefault("LOG_FORMAT", "text"),
		}

		// Initialize silently - if it fails, fall back to console
		if err := Init(driver, config); err != nil {
			// Fall back to console logger
			instance = drivers.NewConsoleLogger()
		}
	})
}

// getEnvOrDefault retrieves an environment variable or returns a default value if not set
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// EnsureInitialized can be called explicitly to guarantee logger initialization,
// though the logger auto-initializes on first use through the init() function.
func EnsureInitialized() {
	Get() // This triggers initialization if not done
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
