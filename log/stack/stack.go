// Package stack provides the stack log driver as a leaf package. The stack
// driver fans a single log record out to several named child channels, which
// it resolves through the canonical log registry at construction time.
// Importing this package (directly or via log/standard) wires the "stack"
// factory.
package stack

import (
	"context"
	"errors"
	"fmt"

	"github.com/velocitykode/velocity/log"
)

// init registers the stack log driver. A stack resolves every requested child
// channel through log.Drivers().Resolve; the default child set ("console" plus
// "daily") requires the file leaf, so log/standard wires both leaves together.
func init() {
	log.Drivers().Register("stack", func(ctx context.Context, cfg log.LogConfig) (log.Logger, error) {
		var channels []string
		if ch, ok := cfg.Config["stack"].([]string); ok {
			channels = ch
		}
		if len(channels) == 0 {
			channels = []string{"console", "daily"}
		}
		// Resolve every requested child; aggregate failures with errors.Join
		// so a typo or missing driver in ANY child takes the whole stack
		// down loudly at boot rather than silently degrading. Continuing
		// with surviving children would mask config errors that must be
		// fixed before the app keeps running.
		var (
			loggers   []log.Logger
			childErrs []error
		)
		for _, name := range channels {
			if name == "stack" {
				continue // prevent recursion
			}
			child, err := log.Drivers().Resolve(ctx, name, log.LogConfig{Driver: name, Config: cfg.Config})
			if err != nil {
				childErrs = append(childErrs, fmt.Errorf("velocity/log: stack driver: child %q: %w", name, err))
				continue
			}
			loggers = append(loggers, child)
		}
		if len(childErrs) > 0 {
			return nil, errors.Join(childErrs...)
		}
		if len(loggers) == 0 {
			return nil, fmt.Errorf("velocity/log: stack driver: no valid channels configured")
		}
		return log.NewStackLogger(loggers...), nil
	})
}
