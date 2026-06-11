package app

import (
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
	Log        contract.Logger
	Exceptions contract.ExceptionHandler
	Crypto     contract.Encryptor
	DB         contract.Database
	Auth       contract.AuthManager
	CSRF       contract.CSRFProtector
	View       contract.ViewEngine

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

	// InsecureFlashCookies opts flash cookies (validation errors / old
	// input) out of the Secure attribute. Set by velocity.New from the
	// already-validated session-cookie config (SESSION_SECURE=false is
	// only permitted in dev/test profiles), and read by both the router's
	// flash-cookie write path and bond's flash-cookie clear path so the
	// two always reach the same Secure decision. Stored inverted so the
	// zero value means Secure (fail-secure for hand-built Services).
	InsecureFlashCookies bool

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
