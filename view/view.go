package view

import (
	"net/http"
	"os"

	"github.com/velocitykode/velocity/bond"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validate"
)

// Props is a type alias for bond.Props
type Props = bond.Props

// Config holds view configuration
type Config struct {
	RootTemplate string
	Version      string
	SSREnabled   bool // Future: SSR support
	SSRURL       string
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

// errorProvider is satisfied by *validate.Errors and any type that
// exposes All() and Old() for validation error rendering.
type errorProvider interface {
	All() map[string]string
	Old() map[string]interface{}
}

// RenderWithErrors renders a component with validation errors and old input
// merged into props. Errors are set as "errors" and old input as "old".
//
//	errors := validate.Check(ctx.Request, validate.Rules{...})
//	if errors.HasErrors() {
//	    view.FromContext(ctx).RenderWithErrors(ctx.Response, ctx.Request,
//	        "Posts/Create", view.Props{}, errors)
//	    return nil
//	}
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

// Validate checks the request against rules and automatically redirects back
// with flashed errors and old input if validation fails.
// Returns true if validation failed (response already sent), false if valid.
//
//	if view.Validate(ctx, validate.Rules{"name": {"required"}, "email": {"required", "email"}}) {
//	    return nil
//	}
func Validate(ctx *router.Context, rules validate.Rules, messages ...validate.Messages) bool {
	errors := validate.Check(ctx.Request, rules, messages...)
	if !errors.HasErrors() {
		return false
	}
	ctx.WithErrors(errors.All())
	ctx.WithInput(errors.Old())
	if e, ok := ctx.View().(*Engine); ok {
		e.Back(ctx.Response, ctx.Request)
	}
	return true
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
