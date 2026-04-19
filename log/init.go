package log

// LogConfig holds configuration for creating a Logger instance.
type LogConfig struct {
	// Driver specifies the log driver: "console" or "file".
	Driver string
	// Config holds driver-specific options (e.g., "path" for file driver).
	Config map[string]any
}
