// Package velocitytest provides test harnesses for building Velocity apps
// in _test.go files. It lives in its own subpackage so production builds do
// not pull in in-memory defaults (memory cache, memory queue, console log,
// APP_ENV=testing) from the root package.
package velocitytest

import (
	"github.com/velocitykode/velocity"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
)

// NewApp constructs a Velocity app with the in-memory defaults needed for
// testing: memory cache, memory queue, console logger, log-mail driver, and
// APP_ENV=testing (which opts out of the APP_KEY requirement). Additional
// options are layered on top and may override these defaults.
func NewApp(opts ...velocity.Option) (*velocity.App, error) {
	config := velocity.Config{
		Name:  "Velocity Test",
		Env:   "testing",
		Debug: true,
		Port:  "0",
		Cache: velocity.CacheConfig{
			Driver: "memory",
			Prefix: "test_cache",
		},
		Log: log.LogConfig{
			Driver: "console",
			Config: make(map[string]any),
		},
		Queue: velocity.QueueConfig{
			Driver: "memory",
		},
		Mail: mail.MailConfig{
			Driver: "log",
		},
	}

	allOpts := []velocity.Option{velocity.WithConfig(config)}
	allOpts = append(allOpts, opts...)
	return velocity.New(allOpts...)
}
