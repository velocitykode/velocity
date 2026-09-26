package app

import (
	"net/http"
	"sync"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/scheduler"
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
//
// First-party and third-party packages that core has no field for register
// their instances in the type-keyed component registry (Register / Get /
// RegisterFor / GetFor, see registry.go). The type-keyed component
// registry gives exact-type keying, qualifier-based multi-instance
// registration, duplicate detection, and registry-owned shutdown, with no
// string keys to collide on.
type Services struct {
	Log    contract.Logger
	Errors contract.ErrorHandler
	Crypto contract.Encryptor
	DB     contract.Database
	Auth   contract.AuthManager
	CSRF   contract.CSRFProtector
	View   contract.ViewEngine

	Cache        contract.CacheManager
	Events       contract.Dispatcher
	Queue        contract.QueueDriver
	Storage      contract.StorageManager
	Scheduler    scheduler.TaskScheduler
	Mail         contract.Mailer
	Notification contract.Notifier
	Validator    contract.Validator

	// RedirectAllowlist exposes the operator-configured cross-origin
	// host allowlist (Router.RedirectAllowedHosts) to redirect helpers
	// that cannot import router. Used by bond.sanitizeRedirectURL to
	// avoid trusting an attacker-controlled r.Host when a fronting
	// proxy is misconfigured. May be nil when the framework is wired
	// without a router (e.g. unit tests).
	RedirectAllowlist contract.RedirectAllowlist

	// CookiePolicy is the Path, Domain, Secure and SameSite every cookie
	// the framework writes or clears carries (session, remember, flash,
	// XSRF token, maintenance bypass and their deletions). Set by
	// velocity.New from the validated session config; the zero value is
	// the secure default (Path "/", Secure, SameSite=Lax), so a hand-built
	// Services still writes Secure cookies.
	CookiePolicy contract.CookiePolicy

	// FlashBag returns the flash bag of the session the request carries,
	// or nil when it carries none (no session scheme is the default, or
	// the request did not pass through the session middleware). The router
	// flashes validation errors and old input into it, the view layer
	// flashes messages into it, and the view engine drains it when it
	// renders the next page. Set by velocity.New; nil on a hand-built
	// Services, which flashes nothing.
	FlashBag func(r *http.Request) contract.FlashBag

	// compMu guards the type-keyed component registry (componentIdx /
	// componentOrder) against concurrent registration and read.
	// Registration may happen at boot or lazily at runtime, so every
	// accessor must be safe for concurrent use. Rule #3.
	compMu sync.RWMutex

	// componentIdx maps a ComponentKey to its position in componentOrder.
	// Lazily initialised on first Register. Reading a missing key is a
	// not-registered error; a present key during Register is a duplicate.
	componentIdx map[ComponentKey]int

	// componentOrder is the append-only list of registered components in
	// registration order. It is never reordered or compacted, so the order
	// is a stable basis for reverse-order shutdown and for ListComponents.
	// componentIdx points into this slice.
	componentOrder []componentEntry
}
