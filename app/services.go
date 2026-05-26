package app

import (
	"fmt"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/exceptions"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/notification"
	"github.com/velocitykode/velocity/orm"
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
// because those packages import router. The contract package is a leaf that
// both sides can import without cycles.
type Services struct {
	Log        log.Logger
	Exceptions exceptions.ExceptionHandler
	Crypto     crypto.Encryptor
	DB         orm.Database
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

	// Extensions holds optional first-party and third-party service instances.
	// Packages register themselves here via ServiceProvider.Register() so that
	// core never needs new fields for each additional package.
	//
	// Prefer RegisterExtension / ExtensionAs over direct map access — the
	// generic helpers give you duplicate-key detection and type-safe reads.
	Extensions map[string]any
}

// RegisterExtension stores an instance under the given key. Returns an error
// if the key is already registered (duplicate registration usually means a
// provider ran twice or two packages clashed on the same key).
func RegisterExtension[T any](s *Services, key string, v T) error {
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
func ExtensionAs[T any](s *Services, key string) (T, error) {
	var zero T
	v, ok := s.Extensions[key]
	if !ok {
		return zero, fmt.Errorf("velocity/app: extension %q not registered", key)
	}
	typed, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("velocity/app: extension %q is %T, not %T", key, v, zero)
	}
	return typed, nil
}
