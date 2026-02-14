package log

import (
	"fmt"

	"github.com/velocitykode/velocity/log/drivers"
)

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
	case "file":
		path := "./storage/logs"
		if p, ok := config["path"].(string); ok {
			path = p
		}
		return drivers.NewFileLogger(path), nil
	case "console":
		return drivers.NewConsoleLogger(), nil
	default:
		return nil, fmt.Errorf("unsupported logger driver: %s", driver)
	}
}
