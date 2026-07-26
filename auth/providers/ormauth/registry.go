package ormauth

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/velocitykode/velocity/auth"
)

// ProviderFactory builds a provider for one registered model name. The
// framework calls it with the options it owns (currently the auth
// manager's hasher); options baked in at registration time are applied
// first, so a call-site option wins over a registration default.
type ProviderFactory func(opts ...Option) (auth.UserProvider, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]ProviderFactory{}
)

// Factory returns a [ProviderFactory] bound to model type T. This is the
// bridge between the configuration string and the compile-time type the
// ORM needs:
//
//	ormauth.MustRegister("Admin", ormauth.Factory[models.Admin]())
//
// defaults are applied before the options the caller passes at
// construction time.
func Factory[T any](defaults ...Option) ProviderFactory {
	return func(opts ...Option) (auth.UserProvider, error) {
		merged := make([]Option, 0, len(defaults)+len(opts))
		merged = append(merged, defaults...)
		merged = append(merged, opts...)

		p := New[T](merged...)
		if err := p.Validate(); err != nil {
			return nil, err
		}
		return p, nil
	}
}

// Register binds a model name to a factory. name is the value the
// application's AUTH_MODEL (or auth.ProviderConfig.Model) carries.
//
// Registering an already-registered name replaces it, which is what lets
// an application override the built-in "User" registration with its own
// model. Safe for concurrent use; the expected call site is a provider's
// Register()/Boot(), or an init() via [MustRegister].
//
// Returns an error on an empty name or a nil factory rather than
// panicking, so a provider that builds its registration from
// configuration can surface the mistake through the normal boot error
// path. Nothing is registered when an error is returned.
func Register(name string, factory ProviderFactory) error {
	if name == "" {
		return errors.New("velocity/ormauth: Register called with an empty model name")
	}
	if factory == nil {
		return fmt.Errorf("velocity/ormauth: Register(%q) called with a nil factory", name)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = factory
	return nil
}

// MustRegister is [Register] for call sites that cannot return an error -
// an init() or a main() - where a failed registration is an
// unrecoverable startup wiring mistake. Mirrors orm.MustRegisterModel.
//
//	func init() {
//	    ormauth.MustRegister("Admin", ormauth.Factory[models.Admin]())
//	}
func MustRegister(name string, factory ProviderFactory) {
	if err := Register(name, factory); err != nil {
		panic(err)
	}
}

// Unregister removes a model name. Returns whether it was registered.
// Intended for tests that install a temporary model.
func Unregister(name string) bool {
	registryMu.Lock()
	defer registryMu.Unlock()
	_, ok := registry[name]
	delete(registry, name)
	return ok
}

// Lookup returns the factory registered under name.
func Lookup(name string) (ProviderFactory, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	factory, ok := registry[name]
	return factory, ok
}

// Registered lists the registered model names in sorted order.
func Registered() []string {
	registryMu.RLock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	registryMu.RUnlock()
	sort.Strings(names)
	return names
}

// Resolve builds the provider registered under name.
//
// An unregistered name is an error naming the model and listing what is
// registered - never a fallback to some other model, because a silent
// fallback is exactly how a mistyped AUTH_MODEL used to authenticate
// against the wrong table.
func Resolve(name string, opts ...Option) (auth.UserProvider, error) {
	if name == "" {
		name = DefaultModelName
	}
	factory, ok := Lookup(name)
	if !ok {
		return nil, fmt.Errorf("velocity/ormauth: model %q is not registered; register it with ormauth.MustRegister(%q, ormauth.Factory[YourModel]()) (registered: %s)",
			name, name, formatRegistered(Registered()))
	}
	provider, err := factory(opts...)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, fmt.Errorf("velocity/ormauth: factory for model %q returned no provider", name)
	}
	return provider, nil
}

// formatRegistered renders the registered set for an error message.
func formatRegistered(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return fmt.Sprint(names)
}
