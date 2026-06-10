// Package view is the Velocity framework's rendering layer.
//
// Consumer code should import view, not bond: view exposes the stable public
// surface (Props, prop helpers, Engine, Render, Redirect) and delegates to an
// underlying protocol implementation. Today that implementation is bond,
// which speaks the Inertia.js wire format; a future release may swap or
// add adapters without changing the view API.
//
// The short version: write view.Props, view.Optional, (*view.Engine).Render —
// never reach into bond directly from application code.
package view

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/velocitykode/velocity/bond"
	"github.com/velocitykode/velocity/router"
)

// Props holds component properties passed to the rendering layer.
type Props = bond.Props

// SharePropsFunc returns props shared across every request (typically user
// identity, flash messages, locale). Registered via (*Engine).SetSharePropsFunc.
type SharePropsFunc = func(r *http.Request) (bond.Props, error)

// LazyProp is evaluated only when explicitly requested via partial reload.
//
// Deprecated: use OptionalProp — mirrors Inertia.js's own Inertia::lazy() sunset.
//
//lint:ignore SA1019 facade re-export of deprecated bond.LazyProp; view.LazyProp carries its own mirrored Deprecated marker.
type LazyProp = bond.LazyProp

// OptionalProp is excluded from the first visit unless explicitly requested.
type OptionalProp = bond.OptionalProp

// AlwaysProp is always included in responses, even during partial reloads.
type AlwaysProp = bond.AlwaysProp

// DeferProp is loaded after the initial page render by the client.
type DeferProp = bond.DeferredProp

// Lazy wraps a value producer that is only evaluated on partial reloads.
//
// Deprecated: use Optional — mirrors Inertia.js's own Inertia::lazy() sunset.
func Lazy(fn func() (any, error)) LazyProp { return bond.Lazy(fn) }

// Optional wraps a value producer that is evaluated only on partial reloads
// where the caller explicitly requests the key.
func Optional(fn func() (any, error)) *OptionalProp { return bond.Optional(fn) }

// Always wraps a value that is included on every render, even during partial
// reloads that would otherwise strip unrelated props.
func Always(value any) AlwaysProp { return bond.Always(value) }

// Defer wraps a value producer evaluated after the initial render; the client
// fetches deferred props once the first paint has committed.
func Defer(fn func() (any, error), group ...string) *DeferProp { return bond.Defer(fn, group...) }

// Config holds view configuration.
type Config struct {
	RootTemplate string
	Version      string

	// Server-side rendering. When SSREnabled is true the view engine
	// pre-renders every full-page response via a Node SSR server and
	// gracefully falls back to CSR on any failure.
	SSREnabled bool
	SSRURL     string        // Defaults to http://127.0.0.1:13714
	SSRTimeout time.Duration // Defaults to 3s
	SSRExcept  []string      // URL prefixes to exclude from SSR

	// Funcs registers template helpers callable from RootTemplate. The
	// canonical use is the bond/vite helper:
	//
	//   helper := vite.New()
	//   cfg.Funcs = template.FuncMap{"vite": helper.Tags}
	//
	// Then app.go.html says {{ vite "resources/js/app.tsx" }}.
	Funcs template.FuncMap
}

// Validate checks the view Config for structural problems. Empty
// RootTemplate / Version are accepted (NewEngine fills in defaults). When
// SSR is enabled the timeout must be strictly positive; a zero or
// negative value would leak the per-render HTTP call into an indefinite
// wait (net/http treats a zero Timeout as "no deadline"). SSRURL is not
// enforced here because NewEngine populates it from the framework
// default when blank.
func (c Config) Validate() error {
	if c.SSREnabled && c.SSRTimeout <= 0 {
		return fmt.Errorf("velocity/view: VIEW_SSR_TIMEOUT must be > 0 when VIEW_SSR_ENABLED=true (got %s)", c.SSRTimeout)
	}
	return nil
}

// Engine wraps a bond.Bond instance and provides the view layer API.
type Engine struct {
	bond *bond.Bond
}

// NewEngine creates a new view Engine with the given configuration.
func NewEngine(config Config) (*Engine, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.RootTemplate == "" {
		config.RootTemplate = defaultTemplate
	}
	if config.Version == "" {
		config.Version = "1"
	}

	b, err := bond.New(bond.Config{
		RootTemplate: config.RootTemplate,
		Version:      config.Version,
		ContainerID:  "app",
		Funcs:        config.Funcs,
		SSR: bond.SSRConfig{
			Enabled: config.SSREnabled,
			URL:     config.SSRURL,
			Timeout: config.SSRTimeout,
			Except:  config.SSRExcept,
		},
	})
	if err != nil {
		return nil, err
	}

	return &Engine{bond: b}, nil
}

// Render renders a component with optional props.
func (e *Engine) Render(w http.ResponseWriter, r *http.Request, component string, props ...Props) error {
	var p Props
	if len(props) > 0 && props[0] != nil {
		p = props[0]
	}
	return e.bond.Render(w, r, component, p)
}

// Share adds a static shared prop.
func (e *Engine) Share(key string, value interface{}) {
	e.bond.Share(key, value)
}

// ShareFunc adds a dynamic shared prop evaluated per-request.
func (e *Engine) ShareFunc(key string, fn func(r *http.Request) (interface{}, error)) {
	e.bond.ShareFunc(key, func(r *http.Request) (any, error) {
		return fn(r)
	})
}

// ShareMultiple adds multiple static shared props.
func (e *Engine) ShareMultiple(props Props) {
	for k, v := range props {
		e.bond.Share(k, v)
	}
}

// SetSharePropsFunc sets a function that returns props to be shared per request.
func (e *Engine) SetSharePropsFunc(fn SharePropsFunc) {
	e.bond.SetSharePropsFunc(fn)
}

// SetEventDispatcher wires the app event bus into the view engine so
// SSR render failures surface as bond.SSRRenderFailed events.
func (e *Engine) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	e.bond.SetEventDispatcher(fn)
}

// Redirect performs an SPA redirect (for internal navigation).
func (e *Engine) Redirect(w http.ResponseWriter, r *http.Request, url string) {
	e.bond.Redirect(w, r, url)
}

// Location performs an external redirect (forces full page load).
func (e *Engine) Location(w http.ResponseWriter, r *http.Request, url string) {
	e.bond.Location(w, r, url)
}

// Back redirects back (SPA navigation).
func (e *Engine) Back(w http.ResponseWriter, r *http.Request) {
	e.bond.Back(w, r)
}

// Middleware returns the Inertia middleware as a router.MiddlewareFunc.
func (e *Engine) Middleware() router.MiddlewareFunc {
	return e.bond.MiddlewareFunc()
}

// Shutdown is a no-op for the view engine; it holds no long-lived resources
// that need draining.
func (e *Engine) Shutdown(ctx context.Context) error {
	return nil
}

// Bond returns the underlying bond.Bond instance.
func (e *Engine) Bond() *bond.Bond {
	return e.bond
}

// Default HTML template for Inertia
const defaultTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Velocity App</title>
    <link rel="stylesheet" href="/build/app.css">
    {{ .inertiaHead }}
</head>
<body>
    {{ .inertia }}
    <script src="/build/app.js"></script>
</body>
</html>`

// Render renders a component using the view engine on the given context.
func Render(ctx *router.Context, component string, props ...Props) error {
	engine := FromContext(ctx)
	if engine == nil {
		return fmt.Errorf("view: engine not configured on context")
	}
	return engine.Render(ctx.Response, ctx.Request, component, props...)
}

// FromContext extracts the *Engine from a router.Context.
// Returns nil if view is not configured, including when the context has
// no service container at all (e.g. a bare test context), so callers
// can rely on the documented nil contract instead of a panic.
func FromContext(ctx *router.Context) *Engine {
	s := ctx.ServicesIfSet()
	if s == nil || s.View == nil {
		return nil
	}
	e, _ := s.View.(*Engine)
	return e
}
