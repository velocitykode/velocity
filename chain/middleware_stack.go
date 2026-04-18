package chain

import (
	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/router"
)

// MiddlewareStack organizes middleware into global, web, and API groups.
// Use it inside a Middleware callback to declare which middleware runs where.
type MiddlewareStack struct {
	global   []router.MiddlewareFunc
	web      []router.MiddlewareFunc
	api      []router.MiddlewareFunc
	services *app.Services
}

// NewMiddlewareStack constructs a MiddlewareStack bound to the given services.
// Callers inside the root velocity package build this during bootstrap; user
// code never constructs one directly.
func NewMiddlewareStack(services *app.Services) *MiddlewareStack {
	return &MiddlewareStack{services: services}
}

// Global appends middleware that runs on every request.
func (m *MiddlewareStack) Global(mw ...router.MiddlewareFunc) {
	m.global = append(m.global, mw...)
}

// Web appends middleware that runs on web (Inertia/HTML) routes.
func (m *MiddlewareStack) Web(mw ...router.MiddlewareFunc) {
	m.web = append(m.web, mw...)
}

// API appends middleware that runs on API routes.
func (m *MiddlewareStack) API(mw ...router.MiddlewareFunc) {
	m.api = append(m.api, mw...)
}

// Services returns the application services for accessing auth, CSRF, view, etc.
func (m *MiddlewareStack) Services() *app.Services {
	return m.services
}

// GlobalMiddleware returns the registered global middleware.
// The root package reads this during bootstrap to install globals on the router.
func (m *MiddlewareStack) GlobalMiddleware() []router.MiddlewareFunc {
	return m.global
}
