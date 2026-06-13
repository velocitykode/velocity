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
		// Prefer the unified "channels" key (what the Manager's stack uses),
		// falling back to the legacy "stack" key. Coerce via log.ToStringSlice
		// so a []any-of-string (the shape JSON/env decoding yields) is accepted
		// the same as a native []string. A present-but-malformed value is a
		// loud error, matching the Manager's fail-loud stance rather than
		// silently degrading to the default child set.
		var channels []string
		raw, ok := cfg.Config["channels"]
		if !ok {
			raw, ok = cfg.Config["stack"]
		}
		if ok {
			ch, valid := log.ToStringSlice(raw)
			if !valid {
				return nil, fmt.Errorf("velocity/log: stack driver: channels must be a list of strings")
			}
			if len(ch) == 0 {
				// A present key that coerces to an empty list is a loud
				// error, matching the Manager path; only an absent key
				// falls back to the default child set.
				return nil, fmt.Errorf("velocity/log: stack driver: no valid channels configured")
			}
			channels = ch
		} else {
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
