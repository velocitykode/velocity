package app

import (
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/exceptions"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/notification"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/scheduler"
	"github.com/velocitykode/velocity/storage"
	"github.com/velocitykode/velocity/validation"
)

// Services holds references to all framework service instances.
// Both the root velocity package and the router package import this
// leaf package, avoiding import cycles.
//
// Auth, CSRF, and View are typed as contract interfaces (not concrete types)
// because those packages import router. Log, Crypto, and DB are likewise
// contract-typed so this leaf need not import log, crypto, or orm; the
// concrete *log logger, *crypto encryptor, and *orm.Manager satisfy the
// matching contract interface with no adapter. The contract package is a
// leaf that both sides can import without cycles.
type Services struct {
	Log        contract.Logger
	Exceptions exceptions.ExceptionHandler
	Crypto     contract.Encryptor
	DB         contract.Database
	Auth       contract.AuthManager
	CSRF       contract.CSRFProtector
	View       contract.ViewEngine

	Cache        cache.CacheManager
	Events       events.Dispatcher
	Queue        queue.Driver
	Storage      storage.StorageManager
	Scheduler    scheduler.TaskScheduler
	Mail         mail.Mailer
	Notification notification.Notifier
	Validator    validation.Validator

	// RedirectAllowlist exposes the operator-configured cross-origin
	// host allowlist (Router.RedirectAllowedHosts) to redirect helpers
	// that cannot import router. Used by bond.sanitizeRedirectURL to
	// avoid trusting an attacker-controlled r.Host when a fronting
	// proxy is misconfigured. May be nil when the framework is wired
	// without a router (e.g. unit tests).
	RedirectAllowlist contract.RedirectAllowlist

	// extMu guards Extensions against concurrent registration and read.
	// Extensions is exported so applications can register their own
	// instances via RegisterExtension at boot; the public API permits
	// later runtime use too (e.g. a chain.Command that lazily registers
	// a sub-service the first time it runs), so every accessor must be
	// safe for concurrent use. Cross-cutting map mutex sweep: rule #3.
	extMu sync.RWMutex

	// Extensions holds optional first-party and third-party service instances.
	// Packages register themselves here via ServiceProvider.Register() so that
	// core never needs new fields for each additional package.
	//
	// Prefer RegisterExtension / ExtensionAs / RangeExtensions over direct
	// map access. The generic helpers give you duplicate-key detection,
	// type-safe reads, and mutex-protected iteration. Direct access to
	// this field is NOT safe for concurrent use.
	Extensions map[string]any
}

// RegisterExtension stores an instance under the given key. Returns an error
// if the key is already registered (duplicate registration usually means a
// provider ran twice or two packages clashed on the same key).
//
// Safe for concurrent use with ExtensionAs / RangeExtensions: every accessor
// serialises through s.extMu.
func RegisterExtension[T any](s *Services, key string, v T) error {
	s.extMu.Lock()
	defer s.extMu.Unlock()
	if s.Extensions == nil {
		s.Extensions = make(map[string]any)
	}
	if _, exists := s.Extensions[key]; exists {
		return fmt.Errorf("velocity/app: extension %q already registered", key)
	}
	s.Extensions[key] = v
	return nil
}

// ExtensionAs retrieves the extension registered under key and asserts it to
// type T. Returns a wrapped error when the key is missing or the stored
// instance does not satisfy T so callers can distinguish the two cases.
//
// Safe for concurrent use; reads s.Extensions under s.extMu.RLock.
func ExtensionAs[T any](s *Services, key string) (T, error) {
	var zero T
	s.extMu.RLock()
	v, ok := s.Extensions[key]
	s.extMu.RUnlock()
	if !ok {
		return zero, fmt.Errorf("velocity/app: extension %q not registered", key)
	}
	typed, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("velocity/app: extension %q is %T, not %T", key, v, zero)
	}
	return typed, nil
}

// RangeExtensions calls fn for every registered extension. fn is invoked
// OUTSIDE the extMu critical section: the map is snapshotted under RLock,
// the lock is released, and fn iterates the snapshot. This means fn is
// free to call RegisterExtension or any other Services method without
// deadlocking, and a slow fn cannot block concurrent RegisterExtension
// writes. Extensions added after the snapshot is taken will not be
// visible to this iteration; call Range again to see them.
//
// Returns false from fn to halt iteration early.
//
// The framework uses this from bootstrap.wireInstanceEvents to push the
// instance event dispatcher into every extension that implements
// contract.EventDispatcherAware.
func (s *Services) RangeExtensions(fn func(key string, v any) bool) {
	s.extMu.RLock()
	snapshot := make(map[string]any, len(s.Extensions))
	for k, v := range s.Extensions {
		snapshot[k] = v
	}
	s.extMu.RUnlock()
	for k, v := range snapshot {
		if !fn(k, v) {
			return
		}
	}
}
