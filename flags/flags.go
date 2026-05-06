// Package flags is the framework's feature-flag adapter surface.
//
// The package intentionally ships only the Provider interface, a top-level
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

// Provider is the minimum surface a feature-flag backend must implement.
// Implementations should return false for unknown flags and must be safe
// for concurrent use.
type Provider interface {
	Enabled(ctx context.Context, name string) bool
}

// providerKey is the unexported context key used by WithProvider / Enabled.
type providerKey struct{}

// WithProvider attaches p to ctx so request-scoped code (middleware,
// handlers) can override the process-wide default for the duration of a
// request. The returned context is safe to pass to Enabled.
func WithProvider(ctx context.Context, p Provider) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, providerKey{}, p)
}

// providerFromContext returns the Provider attached to ctx via WithProvider,
// or nil if none is attached.
func providerFromContext(ctx context.Context) Provider {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(providerKey{}).(Provider)
	return p
}

var (
	defaultMu       sync.RWMutex
	defaultProvider Provider
)

// SetDefault installs p as the process-wide default Provider used by
// Enabled when ctx does not carry one. Pass nil to clear the default.
// Safe for concurrent use.
func SetDefault(p Provider) {
	defaultMu.Lock()
	defaultProvider = p
	defaultMu.Unlock()
}

// Default returns the current process-wide Provider, or nil if unset.
func Default() Provider {
	defaultMu.RLock()
	p := defaultProvider
	defaultMu.RUnlock()
	return p
}

// Enabled reports whether the named flag is on. Resolution order:
//  1. Provider attached to ctx via WithProvider (request-scoped override).
//  2. Process-wide default installed via SetDefault.
//  3. false (safe default - unknown flag stays off).
func Enabled(ctx context.Context, name string) bool {
	if p := providerFromContext(ctx); p != nil {
		return p.Enabled(ctx, name)
	}
	if p := Default(); p != nil {
		return p.Enabled(ctx, name)
	}
	return false
}
