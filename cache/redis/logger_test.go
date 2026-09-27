package redis

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	logdrivers "github.com/velocitykode/velocity/log/drivers"
)

const emptyPrefixWarning = "velocity/cache: redis store configured with empty prefix"

// A store built through the registry writes its startup warnings through
// StoreConfig.Logger, and nothing through the standard library log,
// slog.Default or the fallback logger.
func TestNew_WarnsThroughTheConfigLogger(t *testing.T) {
	stdlib := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)
	mr := miniredis.RunT(t)
	out := &fallbacklogtest.Output{}

	store, err := New(context.Background(), cache.StoreConfig{
		Driver: cache.DriverRedis,
		Host:   mr.Host(),
		Port:   mr.Server().Addr().Port,
		Logger: logdrivers.NewConsoleLoggerTo(out, 0),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.(*RedisStore).Shutdown(context.Background()) })

	if got := strings.Count(out.String(), "WARN: "+emptyPrefixWarning); got != 1 {
		t.Errorf("config logger warn lines = %d, want 1 (%q)", got, out.String())
	}
	if s := stdlib.String() + fallback.String(); s != "" {
		t.Errorf("stdlib / slog.Default / fallback got %q, want nothing", s)
	}
}

// A cache.Manager hands its logger to the stores it builds.
func TestManager_HandsItsLoggerToTheStoresItBuilds(t *testing.T) {
	stdlib := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)
	mr := miniredis.RunT(t)
	out := &fallbacklogtest.Output{}

	m := cache.NewManager(&cache.Config{
		Default: "default",
		Stores: map[string]cache.StoreConfig{
			"default": {Driver: cache.DriverRedis, Host: mr.Host(), Port: mr.Server().Addr().Port},
		},
	})
	m.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	if _, err := m.DefaultStore(); err != nil {
		t.Fatalf("DefaultStore: %v", err)
	}

	if got := strings.Count(out.String(), "WARN: "+emptyPrefixWarning); got != 1 {
		t.Errorf("manager logger warn lines = %d, want 1 (%q)", got, out.String())
	}
	if s := stdlib.String() + fallback.String(); s != "" {
		t.Errorf("stdlib / slog.Default / fallback got %q, want nothing", s)
	}
}

// NewRedisStore, built outside the registry, warns through the fallback
// logger.
func TestNewRedisStore_WarnsThroughTheFallback(t *testing.T) {
	stdlib := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)
	mr := miniredis.RunT(t)

	store, err := NewRedisStore(context.Background(), "", mr.Host(), mr.Server().Addr().Port, "", 0, false)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })

	if got := fallback.Count("WARN", emptyPrefixWarning); got != 1 {
		t.Errorf("fallback warn lines = %d, want 1 (%q)", got, fallback.String())
	}
	if s := stdlib.String(); s != "" {
		t.Errorf("stdlib / slog.Default got %q, want nothing", s)
	}
}
