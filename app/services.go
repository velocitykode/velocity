package app

import (
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
	Exceptions *exceptions.Handler
	Crypto     crypto.Encryptor
	DB         *orm.Manager
	Auth       contract.AuthManager
	CSRF       contract.CSRFProtector
	View       contract.ViewEngine

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
