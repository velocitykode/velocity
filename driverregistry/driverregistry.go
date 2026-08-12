// Package driverregistry defines the canonical pluggable-driver pattern used
// by every Velocity subsystem (cache, storage, mail, notification, queue,
// log, orm, ...). Each subsystem instantiates a Registry parameterised by:
//
//   - D: the driver instance type the subsystem returns to its callers
//     (e.g. cache.Store, storage.Driver, mail.Mailer).
//   - C: the driver configuration shape the subsystem uses to construct an
//     instance (e.g. cache.StoreConfig, storage.DiskConfig, mail.MailConfig).
//
// Drivers register themselves via Registry.Register from an init() in their
// own package; the consuming subsystem creates an instance with
// Registry.Resolve(ctx, name, cfg) inside its Manager.
//
// The registry never holds long-lived driver instances, only factories.
// Lifecycle (start/stop/health) is the responsibility of the instances
// themselves through the optional Closer / HealthChecker interfaces, which
// subsystem managers may probe with type assertions when they enumerate
// their owned drivers.
//
// # Concurrency
//
// All Registry methods are safe for concurrent use. The factory map is
// protected by sync.RWMutex per Velocity security rule #3 (no unprotected
// shared maps). Registration is rare (typically only at process start, from
// init() side effects); resolution happens during request handling and
// uses the read-locked fast path.
//
// # Why a separate package
//
// Centralising the registry semantics here lets every subsystem inherit the
// same guarantees (typed errors, name normalisation, double-registration
// detection, mutex protection) without re-deriving them. A new subsystem
// becomes "extend the pattern", not "reinvent the registry".
package driverregistry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/velocitykode/velocity/contract"
)

// Factory is the constructor a driver author registers. It receives the
// caller's context (so drivers that perform network I/O at construction,
// e.g. an S3 client, can honour deadlines) and the driver-specific
// configuration C, and returns either an instance of D or an error.
//
// Factories must NOT panic on bad config; they must return a typed error.
// Panics in library code are reserved for unrecoverable startup misuse
// (e.g. registering a nil factory) per Velocity security rule #10.
type Factory[D any, C any] func(ctx context.Context, cfg C) (D, error)

// Registry maps lower-cased driver names to factories. The zero value is
// not usable; call New() so the internal map is initialised.
//
// Registry is generic over the driver instance type D and the driver
// configuration type C so each subsystem keeps full type safety at the
// resolution boundary. There is no map[string]any in the hot path.
type Registry[D any, C any] struct {
	subsystem string
	mu        sync.RWMutex
	factories map[string]Factory[D, C]
}

// New constructs an empty Registry. The subsystem string is included in
// every error message so callers can tell which registry produced an error
// (e.g. "velocity/cache: ...", "velocity/storage: ...").
func New[D any, C any](subsystem string) *Registry[D, C] {
	return &Registry[D, C]{
		subsystem: subsystem,
		factories: make(map[string]Factory[D, C]),
	}
}

// normalise lower-cases and trims a driver name so callers cannot
// accidentally register one casing and look up another. This matches the
// existing notification/mail behaviour where init() registrations and
// runtime resolutions are case-insensitive.
func normalise(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// Register installs a factory under name. Callers typically invoke this
// from a package init():
//
//	func init() {
//	    cache.Drivers().Register("redis", newRedisStore)
//	}
//
// Register panics with *contract.RegistrationError when:
//   - name is empty after normalisation;
//   - factory is nil;
//   - name is already registered.
//
// Panicking on these conditions is consistent with the rest of the
// framework and satisfies the "loud at boot" guarantee: a duplicate
// registration is a programming bug that must surface immediately, not
// at the first request.
func (r *Registry[D, C]) Register(name string, factory Factory[D, C]) {
	key := normalise(name)
	if key == "" {
		panic(contract.NewRegistrationError(r.subsystem, "driver name is empty"))
	}
	if factory == nil {
		panic(contract.NewRegistrationError(r.subsystem, fmt.Sprintf("nil factory for %q", name)))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[key]; exists {
		panic(contract.NewRegistrationError(r.subsystem, fmt.Sprintf("driver %q already registered", name)))
	}
	r.factories[key] = factory
}

// Override replaces the factory registered under name (or installs it when
// no prior registration exists). This is intended for tests that swap a
// real driver for a fake; production code should prefer Register so
// duplicate registrations remain loud.
//
// Override returns the previous factory (or nil) so tests can defer
// restoration cleanly:
//
//	prev := reg.Override("redis", fakeFactory)
//	t.Cleanup(func() { reg.Override("redis", prev) })
func (r *Registry[D, C]) Override(name string, factory Factory[D, C]) Factory[D, C] {
	key := normalise(name)
	if key == "" {
		panic(contract.NewRegistrationError(r.subsystem, "driver name is empty"))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	prev := r.factories[key]
	if factory == nil {
		delete(r.factories, key)
	} else {
		r.factories[key] = factory
	}
	return prev
}

// Has reports whether a driver is registered under name (case-insensitive).
func (r *Registry[D, C]) Has(name string) bool {
	key := normalise(name)
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.factories[key]
	return ok
}

// Names returns a sorted snapshot of registered driver names. Useful for
// diagnostics and "did you mean?" hints in error messages.
func (r *Registry[D, C]) Names() []string {
	r.mu.RLock()
	out := make([]string, 0, len(r.factories))
	for k := range r.factories {
		out = append(out, k)
	}
	r.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Resolve looks up the factory registered under name and invokes it with
// cfg. The caller's context is forwarded to the factory.
//
// If no factory is registered, Resolve returns *NotFoundError so callers
// can match on type or use errors.Is via the embedded sentinel.
func (r *Registry[D, C]) Resolve(ctx context.Context, name string, cfg C) (D, error) {
	var zero D
	key := normalise(name)
	if key == "" {
		return zero, &NotFoundError{Subsystem: r.subsystem, Name: name, Available: r.Names()}
	}
	r.mu.RLock()
	factory, ok := r.factories[key]
	r.mu.RUnlock()
	if !ok {
		return zero, &NotFoundError{Subsystem: r.subsystem, Name: name, Available: r.Names()}
	}
	return factory(ctx, cfg)
}

// Closer is the optional interface a driver instance may implement to
// release resources (background goroutines, network connections, file
// handles). Subsystem managers iterate their live driver instances during
// Shutdown and call Close on those that satisfy this interface.
//
// This is intentionally NOT the same as contract.ShutdownAware: Closer is
// for individual driver instances, ShutdownAware is for whole subsystems
// (managers, modules). A driver may implement either, neither, or both.
type Closer interface {
	Close(ctx context.Context) error
}

// HealthChecker is the optional interface a driver instance may implement
// to surface its readiness for health endpoints. The Health method should
// return nil when the driver is functioning, or a typed error describing
// the failure (connection refused, auth missing, ...). Subsystem managers
// can aggregate these results into a /healthz response.
type HealthChecker interface {
	Health(ctx context.Context) error
}
