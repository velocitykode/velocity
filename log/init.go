package log

import (
	"os"
	"strconv"
	"strings"
)

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

	days := 14
	if v := os.Getenv("LOG_DAYS"); v != "" {
		if d, err := strconv.Atoi(v); err == nil {
			days = d
		}
	}

	var stack []string
	if s := os.Getenv("LOG_STACK"); s != "" {
		for _, ch := range strings.Split(s, ",") {
			if ch = strings.TrimSpace(ch); ch != "" {
				stack = append(stack, ch)
			}
		}
	}

	return LogConfig{
		Driver: driver,
		Config: map[string]any{
			"path":   getEnvOrDefault("LOG_PATH", "./storage/logs"),
			"level":  getEnvOrDefault("LOG_LEVEL", "debug"),
			"format": getEnvOrDefault("LOG_FORMAT", "text"),
			"days":   days,
			"stack":  stack,
		},
	}
}
