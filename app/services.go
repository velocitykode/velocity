package app

import (
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/exceptions"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
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
	DB         *orm.Manager
	Log        log.Logger
	Cache      *cache.Manager
	Crypto     crypto.Encryptor
	Events     events.Dispatcher
	Queue      queue.Driver
	Storage    *storage.Manager
	Scheduler  *scheduler.Scheduler
	Mail       mail.Mailer
	Exceptions *exceptions.Handler
	Validator  validation.Validator

	// These use `any` to break import cycles (auth/csrf/view import router).
	// Use typed accessors on velocity.App or router.Context.
	Auth any // *auth.Manager
	CSRF any // *csrf.CSRF
	View any // *view.Engine
}
