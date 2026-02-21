package bus

import (
	"reflect"

	"github.com/velocitykode/velocity/pipeline"
)

// Middleware converts a simple function into a pipeline.Stage[Command].
func Middleware(fn func(cmd Command, next func(Command) error) error) pipeline.Stage[Command] {
	return pipeline.Pipe[Command](fn)
}

// LoggingMiddleware returns middleware that logs command dispatch.
// The logger interface matches the common pattern used in the framework.
func LoggingMiddleware(logger interface {
	Info(msg string, kvs ...any)
}) pipeline.Stage[Command] {
	return Middleware(func(cmd Command, next func(Command) error) error {
		logger.Info("Dispatching command", "type", formatType(cmd))
		err := next(cmd)
		if err != nil {
			logger.Info("Command failed", "type", formatType(cmd), "error", err)
		} else {
			logger.Info("Command completed", "type", formatType(cmd))
		}
		return err
	})
}

func formatType(cmd Command) string {
	if cmd == nil {
		return "<nil>"
	}
	return reflect.TypeOf(cmd).String()
}
