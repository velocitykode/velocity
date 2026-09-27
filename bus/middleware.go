package bus

import (
	"reflect"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/pipeline"
)

// Middleware converts a simple function into a pipeline.Stage[Command].
func Middleware(fn func(cmd Command, next func(Command) error) error) pipeline.Stage[Command] {
	return pipeline.Pipe[Command](fn)
}

// LoggingMiddleware returns middleware that logs command dispatch through
// logger at info level: one line when a command is dispatched and one when
// it completes or fails. A nil logger means the framework's standalone
// fallback logger, which drops info lines.
func LoggingMiddleware(logger contract.Logger) pipeline.Stage[Command] {
	logger = fallbacklog.Resolve(logger)
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
