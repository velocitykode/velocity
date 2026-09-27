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
// logger: one info line when a command is dispatched, then one info line
// when it completes or one error line, with the error, when it fails, so a
// failed command still shows at a warn or error log level. A nil logger
// means the framework's standalone fallback logger, which drops info lines
// and writes the error line.
func LoggingMiddleware(logger contract.Logger) pipeline.Stage[Command] {
	logger = fallbacklog.Resolve(logger)
	return Middleware(func(cmd Command, next func(Command) error) error {
		logger.Info("Dispatching command", "type", formatType(cmd))
		err := next(cmd)
		if err != nil {
			logger.Error("Command failed", "type", formatType(cmd), "error", err)
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
