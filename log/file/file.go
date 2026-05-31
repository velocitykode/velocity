// Package file provides the file and daily log drivers as a leaf package so
// the log root never pulls in their transitive dependencies (the async
// goroutine helper used for background log-file cleanup). Importing this
// package (directly or via log/standard) wires the "file" and "daily"
// factories into the canonical log registry.
package file

import (
	"context"

	"github.com/velocitykode/velocity/log"
)

// init registers the file and daily log drivers into the canonical log
// registry. "daily" is an alias of "file": both share one factory because
// the file logger already rotates daily. The factory honours the same
// "path"/"days"/"level" config keys and redactor opt-ins the root drivers
// do, so loggers produced through this leaf are identical to the ones the
// registry produced before the driver split.
func init() {
	factory := func(_ context.Context, cfg log.LogConfig) (log.Logger, error) {
		path := "./storage/logs"
		if p, ok := cfg.Config["path"].(string); ok {
			path = p
		}
		days := 14
		if d, ok := cfg.Config["days"].(int); ok {
			days = d
		}
		return log.WrapWithRedactors(NewFileLogger(path, days, log.ExtractLevel(cfg.Config)), cfg), nil
	}
	log.Drivers().Register("file", factory)
	log.Drivers().Register("daily", factory)
}
