package velocity

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/scheduler"
)

// testConfigWithTimezone mirrors NewTestApp's in-memory config with an
// explicit app timezone.
func testConfigWithTimezone(tz string) Config {
	return Config{
		Env:      "testing",
		Debug:    true,
		Port:     "0",
		Timezone: tz,
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
}

// TestNew_InvalidAppTimezone verifies an unloadable APP_TIMEZONE fails
// construction instead of silently running with a wrong presentation zone.
func TestNew_InvalidAppTimezone(t *testing.T) {
	_, err := New(WithConfig(testConfigWithTimezone("Not/AZone")))
	if err == nil {
		t.Fatal("New() succeeded with invalid timezone, want error")
	}
	if !strings.Contains(err.Error(), "invalid app timezone") {
		t.Errorf("error = %v, want mention of invalid app timezone", err)
	}
}

// TestNew_AppTimezoneWiresScheduler verifies the app timezone reaches cron
// evaluation and time.Local; persistence is covered separately (it never
// reads this knob).
func TestNew_AppTimezoneWiresScheduler(t *testing.T) {
	app, err := New(WithConfig(testConfigWithTimezone("UTC")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer app.Shutdown(context.Background())

	sched, ok := app.Scheduler.(*scheduler.Scheduler)
	if !ok {
		t.Fatalf("Scheduler is %T, want *scheduler.Scheduler", app.Scheduler)
	}
	if got := sched.Timezone(); got != time.UTC {
		t.Errorf("scheduler timezone = %v, want UTC", got)
	}
	if time.Local != time.UTC {
		t.Errorf("time.Local = %v, want UTC", time.Local)
	}
}
