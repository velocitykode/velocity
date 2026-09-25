package chain

import (
	"net/http"
	"slices"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/router"
)

// Routing provides a declarative API for registering web and API routes.
// It wraps the underlying router and applies middleware from the MiddlewareStack.
type Routing struct {
	router     *router.VelocityRouterV2
	middleware *MiddlewareStack
}

// NewRouting constructs a Routing bound to the given router and middleware
// stack. Called from the root velocity package during bootstrap.
func NewRouting(r *router.VelocityRouterV2, m *MiddlewareStack) *Routing {
	return &Routing{router: r, middleware: m}
}

// Web creates a route group with web middleware applied.
func (r *Routing) Web(fn func(router.Router)) {
	g := r.router.Group("", fn)
	g.Use(r.middleware.web...)
}

// API creates a route group with the given prefix and API middleware
// applied, and registers prefix as an API prefix on the application's
// error handler (Services.Errors): every request whose path starts with
// prefix answers its errors as problem+json whatever its Accept header,
// unless a JSONWhen predicate on the handler decides instead. The prefix
// is added as given, once, beside any prefixes already configured; an
// empty prefix registers nothing. With no error handler (a standalone
// router) nothing is registered.
func (r *Routing) API(prefix string, fn func(router.Router)) {
	r.registerAPIPrefix(prefix)
	g := r.router.Group(prefix, fn)
	g.Use(r.middleware.api...)
}

// registerAPIPrefix appends prefix to the error handler's API prefixes
// unless it is empty or already listed.
func (r *Routing) registerAPIPrefix(prefix string) {
	if prefix == "" || r.middleware == nil || r.middleware.services == nil {
		return
	}
	h := r.middleware.services.Errors
	if h == nil {
		return
	}
	prefixes := h.GetAPIPrefixes()
	if slices.Contains(prefixes, prefix) {
		return
	}
	h.SetAPIPrefixes(append(prefixes, prefix)...)
}

// Health registers a GET endpoint that returns 200 "OK".
func (r *Routing) Health(path string) {
	r.router.Get(path, func(c *router.Context) error {
		c.Response.WriteHeader(http.StatusOK)
		_, err := c.Response.Write([]byte("OK"))
		return err
	})
}

// Static enables serving static files from the given directory.
func (r *Routing) Static(dir string) {
	r.router.Static(dir)
}

// Services returns the application services.
func (r *Routing) Services() *app.Services {
	return r.middleware.services
}

// Router returns the underlying router for advanced use cases.
func (r *Routing) Router() *router.VelocityRouterV2 {
	return r.router
}
