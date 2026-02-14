package bond

import "net/http"

// Props holds component properties
type Props map[string]any

// SharedPropFunc is evaluated per-request for dynamic shared props
type SharedPropFunc func(r *http.Request) (any, error)

// LazyProp is evaluated only when explicitly requested via partial reload
type LazyProp struct {
	fn func() (any, error)
}

// Lazy creates a lazy prop that is only evaluated when explicitly requested
func Lazy(fn func() (any, error)) LazyProp {
	return LazyProp{fn: fn}
}

// Evaluate resolves the lazy prop value
func (l LazyProp) Evaluate() (any, error) {
	return l.fn()
}

// DeferredProp is loaded after the initial page render by the client
type DeferredProp struct {
	fn    func() (any, error)
	group string
}

// Defer creates a deferred prop with an optional group name
// Deferred props are not included in the initial response but are
// fetched by the client after the page renders
func Defer(fn func() (any, error), group ...string) DeferredProp {
	g := "default"
	if len(group) > 0 && group[0] != "" {
		g = group[0]
	}
	return DeferredProp{fn: fn, group: g}
}

// Evaluate resolves the deferred prop value
func (d DeferredProp) Evaluate() (any, error) {
	return d.fn()
}

// Group returns the deferred prop's group name
func (d DeferredProp) Group() string {
	return d.group
}

// AlwaysProp is always included in responses, even during partial reloads
type AlwaysProp struct {
	value any
}

// Always creates a prop that is always included, even in partial reloads
func Always(value any) AlwaysProp {
	return AlwaysProp{value: value}
}

// Value returns the always prop's value
func (a AlwaysProp) Value() any {
	return a.value
}

// OptionalProp is excluded from the first visit unless explicitly requested
// Similar to LazyProp but semantically different for API clarity
type OptionalProp struct {
	fn func() (any, error)
}

// Optional creates an optional prop (same behavior as Lazy)
func Optional(fn func() (any, error)) OptionalProp {
	return OptionalProp{fn: fn}
}

// Evaluate resolves the optional prop value
func (o OptionalProp) Evaluate() (any, error) {
	return o.fn()
}
