package router

import (
	"github.com/velocitykode/velocity/app"
)

// Service retrieves the registry component registered under the exact type T
// from the request's service container. It is the handler-side accessor for
// components stored via app.Register; first-party SDK From(ctx) helpers are
// expected to build on top of it.
//
// Lookup is by EXACT type: Service[SomeIface] only finds an entry registered
// with T=SomeIface, never a concrete value that merely satisfies it. This
// mirrors app.Get.
//
// It never panics. If c is nil or services were never wired onto it, it
// returns the zero T and a *contract.ServiceNotConfiguredError naming
// "services"; if the component is not registered it returns the zero T and
// the error from app.Get, which names the component key.
func Service[T any](c *Context) (T, error) {
	return ServiceFor[T, app.Default](c)
}

// ServiceFor retrieves the registry component registered under the exact type T
// qualified by marker type Q. It is the qualified form of Service, delegating to
// app.GetFor; see Service for the exact-match and no-panic semantics.
//
// As with Service, a missing service container and a missing component both
// return errors and never panic.
func ServiceFor[T any, Q any](c *Context) (T, error) {
	s, err := c.Services()
	if err != nil {
		var zero T
		return zero, err
	}
	return app.GetFor[T, Q](s)
}
