package app

import (
	"github.com/velocitykode/velocity/cache"
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
// Fields typed as `any` break import cycles for packages that import
// the router package (auth, csrf, view). The root velocity.App and
// the router.Context provide typed accessors for these.
type Services struct {
	Log        log.Logger
	Exceptions *exceptions.Handler
	Crypto     crypto.Encryptor
	DB         *orm.Manager

	// These use `any` to break import cycles (auth/csrf/view import router).
	// Use typed accessors on velocity.App or router.Context.
	Auth any // *auth.Manager
	CSRF any // *csrf.CSRF
	View any // *view.Engine

	Cache        *cache.Manager
	Events       events.Dispatcher
	Queue        queue.Driver
	Storage      *storage.Manager
	Scheduler    *scheduler.Scheduler
	Mail         mail.Mailer
	Notification *notification.Manager
	Validator    validation.Validator

	// Extensions holds optional first-party and third-party service instances.
	// Packages register themselves here via ServiceProvider.Register() so that
	// core never needs new fields for each additional package.
	Extensions map[string]any
}
