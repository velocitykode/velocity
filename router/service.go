package router

import (
	"errors"

	"github.com/velocitykode/velocity/app"
)

// errServicesNotSet is returned when a handler asks for a registry component
// but the service container was never wired onto the Context.
var errServicesNotSet = errors.New("velocity/router: services not set on context")

// Service retrieves the registry component registered under the exact type T
// from the request's service container. It is the handler-side accessor for
// components stored via app.Register; first-party SDK From(ctx) helpers are
// expected to build on top of it.
//
// Lookup is by EXACT type: Service[SomeIface] only finds an entry registered
// with T=SomeIface, never a concrete value that merely satisfies it. This
// mirrors app.Get.
//
// It never panics. If services were never wired onto the Context it returns the
// zero T and errServicesNotSet; if the component is not registered it returns
// the zero T and the error from app.Get.
func Service[T any](c *Context) (T, error) {
	s := c.ServicesIfSet()
	if s == nil {
		var zero T
		return zero, errServicesNotSet
	}
	return app.Get[T](s)
}

// ServiceFor retrieves the registry component registered under the exact type T
// qualified by marker type Q. It is the qualified form of Service, delegating to
// app.GetFor; see Service for the exact-match and no-panic semantics.
//
// As with Service, a missing service container and a missing component both
// return errors and never panic.
func ServiceFor[T any, Q any](c *Context) (T, error) {
	s := c.ServicesIfSet()
	if s == nil {
		var zero T
		return zero, errServicesNotSet
	}
	return app.GetFor[T, Q](s)
}
