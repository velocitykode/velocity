package velocity

import (
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// The statements the ORM runs while it is built (SQLite's connection
// PRAGMAs) write their query log lines through the app logger, which exists
// before the database does, and never through the fallback logger.
func TestNew_DBInitStatementsLogThroughTheAppLogger(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	_, capture := newLoggerWiringApp(t, func(c *Config) {
		c.DB = DBConfig{Connection: "sqlite", Database: ":memory:", LogQueries: true, SlowThreshold: time.Nanosecond}
	})

	for _, pragma := range []string{"PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL"} {
		n := 0
		capture.mu.Lock()
		for _, e := range capture.entries {
			if e.level == "warn" && e.msg == "velocity/orm: slow query" && kvValue(e.kvs, "query") == pragma {
				n++
			}
		}
		capture.mu.Unlock()
		if n != 1 {
			t.Errorf("app logger slow query lines for %q = %d, want 1", pragma, n)
		}
	}
	if out := fallback.String(); strings.Contains(out, "PRAGMA") {
		t.Errorf("fallback logger got the init statements: %q", out)
	}
}

// kvValue returns the value logged under key, or nil.
func kvValue(kvs []any, key string) any {
	for i := 0; i+1 < len(kvs); i += 2 {
		if kvs[i] == key {
			return kvs[i+1]
		}
	}
	return nil
}
