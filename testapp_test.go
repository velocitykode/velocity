package velocity

import (
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
)

// NewTestApp is the internal test-only constructor used by root-package
// tests. External consumers should import velocitytest.NewApp instead;
// keeping this here (in a _test.go file) ensures the defaults never ship in
// a production binary. Keep velocitytest.NewApp in sync with this config.
func NewTestApp(opts ...Option) (*App, error) {
	config := Config{
		Env:   "testing",
		Debug: true,
		Port:  "0",
		Cache: CacheConfig{
			Driver: "memory",
			Prefix: "test_cache",
		},
		Log: log.LogConfig{
			Driver: "console",
			Config: make(map[string]any),
		},
		Queue: QueueConfig{
			Driver: "memory",
		},
		Mail: mail.MailConfig{
			Driver: "log",
		},
	}

	allOpts := []Option{WithConfig(config)}
	allOpts = append(allOpts, opts...)
	return New(allOpts...)
}
