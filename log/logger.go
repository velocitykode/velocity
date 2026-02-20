package log

import (
	"fmt"

	"github.com/velocitykode/velocity/log/drivers"
)

// Closer is an optional interface loggers may implement for graceful shutdown.
type Closer interface {
	Close() error
}

// Level represents the severity of a log message
type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
	FATAL
)

// Logger defines the interface for all log implementations
type Logger interface {
	Debug(msg string, kvs ...any)
	Info(msg string, kvs ...any)
	Warn(msg string, kvs ...any)
	Error(msg string, kvs ...any)
	Fatal(msg string, kvs ...any)
}

// NewLogger creates a new Logger instance with the given configuration.
// This is the preferred way to create loggers instead of using the global Init().
func NewLogger(config LogConfig) (Logger, error) {
	return createDriver(config.Driver, config.Config)
}

// createDriver creates a Logger from a driver name and config map.
func createDriver(driver string, config map[string]any) (Logger, error) {
	switch driver {
	case "file", "daily":
		path := "./storage/logs"
		if p, ok := config["path"].(string); ok {
			path = p
		}
		days := 14
		if d, ok := config["days"].(int); ok {
			days = d
		}
		return drivers.NewFileLogger(path, days), nil
	case "console":
		return drivers.NewConsoleLogger(), nil
	case "stack":
		var channels []string
		if ch, ok := config["stack"].([]string); ok {
			channels = ch
		}
		if len(channels) == 0 {
			channels = []string{"console", "daily"}
		}
		var loggers []Logger
		for _, name := range channels {
			if name == "stack" {
				continue // prevent recursion
			}
			l, err := createDriver(name, config)
			if err != nil {
				continue
			}
			loggers = append(loggers, l)
		}
		if len(loggers) == 0 {
			return nil, fmt.Errorf("stack driver: no valid channels configured")
		}
		return NewStackLogger(loggers...), nil
	case "null":
		return NewNullLogger(), nil
	default:
		return nil, fmt.Errorf("unsupported logger driver: %s", driver)
	}
}
