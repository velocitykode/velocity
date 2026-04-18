package view

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/velocitykode/velocity/bond"
	"github.com/velocitykode/velocity/router"
)

// Props is a type alias for bond.Props
type Props = bond.Props

// Config holds view configuration
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

// DefaultViewConfig returns a Config with sensible defaults.
func DefaultViewConfig() Config {
	return Config{
		SSRURL:     "http://127.0.0.1:13714",
		SSRTimeout: 3 * time.Second,
	}
}

// Validate checks that the Config is internally consistent.
func (c Config) Validate() error {
	if c.SSREnabled && c.SSRURL == "" {
		return fmt.Errorf("view: SSR URL is required when SSR is enabled")
	}
	return nil
}

// SharePropsFunc is a function that returns props to be shared per request
type SharePropsFunc func(r *http.Request) (Props, error)

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

// errorProvider is satisfied by *validation.Result and any type that
// exposes All() and Old() for validation error rendering.
type errorProvider interface {
	All() map[string]string
	Old() map[string]interface{}
}

// RenderWithErrors renders a component with validation errors and old input
// merged into props. Errors are set as "errors" and old input as "old".
func (e *Engine) RenderWithErrors(w http.ResponseWriter, r *http.Request, component string, props Props, errors errorProvider) error {
	if props == nil {
		props = Props{}
	}
	props["errors"] = errors.All()
	if old := errors.Old(); len(old) > 0 {
		props["old"] = old
	}
	return e.bond.Render(w, r, component, props)
}

// Render renders an Inertia component using the view engine on the given context.
func Render(ctx *router.Context, component string, props ...Props) error {
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

// LoadTemplateFromFile loads the root template from a file
func LoadTemplateFromFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}
