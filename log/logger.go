package log

import (
	"context"
	"strings"
)

// Shutdowner is an optional interface loggers may implement for graceful shutdown.
type Shutdowner interface {
	Shutdown(ctx context.Context) error
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

// Logger defines the interface for all log implementations.
//
// Implementations must pass logtest.RunLoggerContractTests. See logtest
// for the executable specification.
type Logger interface {
	Debug(msg string, kvs ...any)
	Info(msg string, kvs ...any)
	Warn(msg string, kvs ...any)
	Error(msg string, kvs ...any)
	Fatal(msg string, kvs ...any)
}

// NewLogger creates a new Logger instance with the given configuration.
// This is the preferred way to create loggers instead of using the global Init().
//
// The driver name is resolved through the canonical driver registry, so
// third-party drivers registered via Drivers().Register are available
// alongside the built-in console / file / daily / stack / null drivers.
func NewLogger(config LogConfig) (Logger, error) {
	return NewLoggerWithContext(context.Background(), config)
}

// NewLoggerWithContext is the context-aware variant of NewLogger. The
// ctx is forwarded to the driver factory; the stack driver propagates it
// when resolving its child channels.
func NewLoggerWithContext(ctx context.Context, config LogConfig) (Logger, error) {
	return driverRegistry.Resolve(ctx, config.Driver, config)
}

// parseLevel converts a level string to its numeric value.
func parseLevel(s string) int {
	switch strings.ToLower(s) {
	case "info":
		return int(INFO)
	case "warn", "warning":
		return int(WARN)
	case "error":
		return int(ERROR)
	case "fatal":
		return int(FATAL)
	default:
		return int(DEBUG)
	}
}
