package velocity

import (
	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/chain"
)

// This file gives application code a single import (velocity) for the
// declarative bootstrap API. The types below live in chain/ and app/ to
// break the import cycle between router (which needs app.Services) and
// consumer callbacks (which reference the chain types via *App methods).
// That split is a framework-internal concern; consumers should not have
// to learn it. These aliases re-expose the types under the root package
// so handler code, provider code, and generated scaffolding all stay
// inside the "velocity" namespace.
//
// Unlike view/bond, there is no swappable implementation hiding behind
// these names — chain/ and app/ are the only homes these types will
// ever have. The aliases exist purely for ergonomics: one namespace for
// application code, so routes.go and app.go don't need to import both
// velocity and chain.

// Declarative bootstrap types — used by *App chain methods.
type (
	// Routing is the argument to App.Routes(fn). Use (*Routing).Web,
	// (*Routing).API, (*Routing).Health, and (*Routing).Static to
	// register routes; (*Routing).Router returns the underlying
	// *VelocityRouterV2 for escape-hatch use.
	Routing = chain.Routing

	// MiddlewareStack is the argument to App.Middleware(fn). Use
	// (*MiddlewareStack).Global, .Web, .API to add middleware to the
	// respective chains.
	MiddlewareStack = chain.MiddlewareStack

	// ProviderRegistry is the argument to App.Providers(fn). Use
	// (*ProviderRegistry).Add to register ServiceProvider implementations
	// from the chain.
	ProviderRegistry = chain.ProviderRegistry

	// Commands is the argument to App.Commands(fn). Use (*Commands).Add
	// to register a custom CLI command reachable via `vel run <name>`.
	Commands = chain.Commands
)

// Optional provider interfaces — implemented by ServiceProvider types to
// auto-wire routes, middleware, events, schedules, or commands during
// bootstrap. Implementation is structural (no explicit "implements"
// declaration needed), but type assertions and compile-time conformance
// checks use these names.
type (
	RouteProvider      = chain.RouteProvider
	MiddlewareProvider = chain.MiddlewareProvider
	EventProvider      = chain.EventProvider
	ScheduleProvider   = chain.ScheduleProvider
	CommandProvider    = chain.CommandProvider
)

// Service container types.
type (
	// Services is the DI container holding every non-router service
	// (logger, cache, queue, auth, ORM manager, etc.). Embedded on *App
	// and shared with router.Context via Router.SetServices.
	Services = app.Services

	// ServiceProvider is the lifecycle interface for modular service
	// registration. Implement Register (bind services), Boot (wire
	// dependencies), and Shutdown (teardown). Register in order via
	// App.Providers(fn) or velocity.New(WithProviders(...)).
	ServiceProvider = app.ServiceProvider
)
