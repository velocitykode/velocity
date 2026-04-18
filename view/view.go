// Package view is the framework's view layer — a thin façade over bond/ that
// translates framework configuration into an Inertia adapter and implements
// contract.ViewEngine for router.Context.
//
// Prop helpers (Always, Lazy, Optional, Defer, Merge, Once, Scroll) live in
// bond/ — the Inertia protocol package. Import bond directly when you need
// them in handler code; view does not re-export them.
package view

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/velocitykode/velocity/bond"
	"github.com/velocitykode/velocity/router"
)

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
}

// Engine wraps a bond.Bond instance and provides the view layer API.
type Engine struct {
	bond *bond.Bond
}

// NewEngine creates a new view Engine with the given configuration.
func NewEngine(config Config) (*Engine, error) {
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
func (e *Engine) Render(w http.ResponseWriter, r *http.Request, component string, props ...bond.Props) error {
	var p bond.Props
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
func (e *Engine) ShareMultiple(props bond.Props) {
	for k, v := range props {
		e.bond.Share(k, v)
	}
}

// SetSharePropsFunc sets a function that returns props to be shared per request.
func (e *Engine) SetSharePropsFunc(fn func(r *http.Request) (bond.Props, error)) {
	e.bond.SetSharePropsFunc(fn)
}

// SetEventDispatcher wires the app event bus into the view engine so
// SSR render failures surface as bond.SSRRenderFailed events.
func (e *Engine) SetEventDispatcher(fn func(event interface{}) error) {
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

// Render renders an Inertia component using the view engine on the given context.
func Render(ctx *router.Context, component string, props ...bond.Props) error {
	engine := FromContext(ctx)
	if engine == nil {
		return fmt.Errorf("view: engine not configured on context")
	}
	return engine.Render(ctx.Response, ctx.Request, component, props...)
}

// FromContext extracts the *Engine from a router.Context.
// Returns nil if view is not configured.
func FromContext(ctx *router.Context) *Engine {
	e, _ := ctx.View().(*Engine)
	return e
}
