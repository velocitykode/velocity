package log

import (
	"os"
	"sync"

	"github.com/joho/godotenv"
	"github.com/velocitykode/velocity/pkg/log/drivers"
)

var initOnce sync.Once

// init automatically initializes the logger on package import.
// Loads .env file if present and configures logger based on environment variables:
// - LOG_DRIVER: Driver to use (console, file). Defaults to console.
// - LOG_PATH: Directory for file logs. Defaults to ./storage/logs
// - LOG_LEVEL: Minimum log level. Defaults to debug.
// - LOG_FORMAT: Output format. Defaults to text.
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
// though the logger auto-initializes on first use through the init() function
func EnsureInitialized() {
	Get() // This triggers initialization if not done
}
