package velocity

import (
	"os"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

func TestConfigFromEnv_UnsetAppEnvStaysEmptyAndFailsClosed(t *testing.T) {
	t.Setenv("APP_ENV", "")
	_ = os.Unsetenv("APP_ENV")

	cfg := ConfigFromEnv()
	if cfg.Env != "" {
		t.Fatalf("unset APP_ENV should stay empty, got %q", cfg.Env)
	}
	if contract.IsDevOrTestEnv(cfg.Env) {
		t.Fatalf("unset APP_ENV must not be classified as dev/test")
	}
	if contract.IsTestingEnv(cfg.Env) {
		t.Fatalf("unset APP_ENV must not be classified as testing")
	}
}

// TestConfigFromEnv_DBTimezoneAndAppTimezone pins the two timezone knobs:
// DB_TIMEZONE flows to DBConfig.TimeZone (database SESSION timezone only),
// APP_TIMEZONE flows to Config.Timezone (presentation) and defaults to UTC.
func TestConfigFromEnv_DBTimezoneAndAppTimezone(t *testing.T) {
	t.Setenv("DB_TIMEZONE", "Asia/Karachi")
	t.Setenv("APP_TIMEZONE", "")
	_ = os.Unsetenv("APP_TIMEZONE")

	cfg := ConfigFromEnv()
	if cfg.DB.TimeZone != "Asia/Karachi" {
		t.Errorf("DB.TimeZone = %q, want Asia/Karachi", cfg.DB.TimeZone)
	}
	if cfg.Timezone != "UTC" {
		t.Errorf("Timezone default = %q, want UTC", cfg.Timezone)
	}

	t.Setenv("APP_TIMEZONE", "Asia/Karachi")
	if got := ConfigFromEnv().Timezone; got != "Asia/Karachi" {
		t.Errorf("Timezone = %q, want Asia/Karachi", got)
	}
}
