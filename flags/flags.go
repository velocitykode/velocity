// Package flags is the framework's feature-flag adapter surface.
//
// The package intentionally ships only the Driver interface, a top-level
// Enabled helper, request-scoped context attachment, a process-wide default
// slot, and a memory driver for tests/dev. Production deployments are
// expected to plug in a third-party SaaS (LaunchDarkly, Unleash, PostHog,
// Statsig, Flagsmith) or a community adapter behind the same interface.
//
// Higher-level concerns - rollout strategies, percentage hashing, cohort
// targeting, registries, listeners, and admin UI - are deliberately out of
// scope; see HIGH-VALUE-FEATURES.md section 8 (revised 2026-05-06) for the
// rationale.
package flags

import (
	"context"
	"sync"
)

// Driver is the minimum surface a feature-flag backend must implement.
// Implementations should return false for unknown flags and must be safe
// for concurrent use.
type Driver interface {
	Enabled(ctx context.Context, name string) bool
}

// driverKey is the unexported context key used by WithDriver / Enabled.
type driverKey struct{}

// WithDriver attaches d to ctx so request-scoped code (middleware,
// handlers) can override the process-wide default for the duration of a
// request. The returned context is safe to pass to Enabled.
func WithDriver(ctx context.Context, d Driver) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, driverKey{}, d)
}

// driverFromContext returns the Driver attached to ctx via WithDriver,
// or nil if none is attached.
func driverFromContext(ctx context.Context) Driver {
	if ctx == nil {
		return nil
	}
	d, _ := ctx.Value(driverKey{}).(Driver)
	return d
}

var (
	defaultMu     sync.RWMutex
	defaultDriver Driver
)

// SetDefault installs d as the process-wide default Driver used by
// Enabled when ctx does not carry one. Pass nil to clear the default.
// Safe for concurrent use.
func SetDefault(d Driver) {
	defaultMu.Lock()
	defaultDriver = d
	defaultMu.Unlock()
}

// Default returns the current process-wide Driver, or nil if unset.
func Default() Driver {
	defaultMu.RLock()
	d := defaultDriver
	defaultMu.RUnlock()
	return d
}

// Enabled reports whether the named flag is on. Resolution order:
//  1. Driver attached to ctx via WithDriver (request-scoped override).
//  2. Process-wide default installed via SetDefault.
//  3. false (safe default - unknown flag stays off).
func Enabled(ctx context.Context, name string) bool {
	if d := driverFromContext(ctx); d != nil {
		return d.Enabled(ctx, name)
	}
	if d := Default(); d != nil {
		return d.Enabled(ctx, name)
	}
	return false
}
