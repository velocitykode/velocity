package velocity

import (
	"net/http"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/router"
)

// Routing provides a declarative API for registering web and API routes.
// It wraps the underlying router and applies middleware from the MiddlewareStack.
type Routing struct {
	router     *router.VelocityRouterV2
	middleware *MiddlewareStack
}

// Web creates a route group with web middleware applied.
func (r *Routing) Web(fn func(router.Router)) {
	g := r.router.Group("", fn)
	g.Use(r.middleware.web...)
}

// API creates a route group with the given prefix and API middleware applied.
func (r *Routing) API(prefix string, fn func(router.Router)) {
	g := r.router.Group(prefix, fn)
	g.Use(r.middleware.api...)
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
