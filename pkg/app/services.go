package app

import (
	"github.com/velocitykode/velocity/pkg/cache"
	"github.com/velocitykode/velocity/pkg/crypto"
	"github.com/velocitykode/velocity/pkg/events"
	"github.com/velocitykode/velocity/pkg/exceptions"
	"github.com/velocitykode/velocity/pkg/log"
	"github.com/velocitykode/velocity/pkg/mail"
	"github.com/velocitykode/velocity/pkg/orm"
	"github.com/velocitykode/velocity/pkg/queue"
	"github.com/velocitykode/velocity/pkg/scheduler"
	"github.com/velocitykode/velocity/pkg/storage"
	"github.com/velocitykode/velocity/pkg/validation"
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
