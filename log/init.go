package log

import "fmt"

// LogConfig holds configuration for creating a Logger instance.
type LogConfig struct {
	// Driver specifies the log driver: "console" or "file".
	Driver string
	// Config holds driver-specific options (e.g., "path" for file driver).
	Config map[string]any
}

// DefaultLogConfig returns a LogConfig with sensible defaults.
func DefaultLogConfig() LogConfig {
	return LogConfig{
		Driver: "console",
		Config: map[string]any{
			"level": "debug",
		},
	}
}

// Validate checks that the LogConfig is valid.
func (c LogConfig) Validate() error {
	switch c.Driver {
	case "console", "file", "stack", "null", "":
	default:
		return fmt.Errorf("log: unsupported driver %q", c.Driver)
	}
	if c.Driver == "file" {
		if path, _ := c.Config["path"].(string); path == "" {
			return fmt.Errorf("log: file driver requires a path")
		}
	}
	return nil
}
