package velocity

import (
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/queue"
)

// NewTestApp is the internal test-only constructor used by root-package
// tests. External consumers should import velocitytest.NewApp instead;
// keeping this here (in a _test.go file) ensures the defaults never ship in
// a production binary. Keep velocitytest.NewApp in sync with this config.
func NewTestApp(opts ...Option) (*App, error) {
	allOpts := []Option{WithConfig(testAppConfig())}
	allOpts = append(allOpts, opts...)
	return New(allOpts...)
}

// testAppConfig is the config NewTestApp starts from.
func testAppConfig() Config {
	return Config{
		Env:   "testing",
		Debug: true,
		Port:  "0",
		Cache: cache.SingleStoreConfig("test_cache", cache.StoreConfig{Driver: "memory"}),
		Log: log.Config{
			Driver: "console",
			Config: make(map[string]any),
		},
		Queue: queue.Config{
			Driver: "memory",
		},
		Mail: mail.Config{
			Driver: "log",
		},
	}
}
