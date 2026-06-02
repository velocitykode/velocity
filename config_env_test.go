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
