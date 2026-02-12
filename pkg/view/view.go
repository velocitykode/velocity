package view

import (
	"net/http"
	"os"

	"github.com/velocitykode/velocity/pkg/bond"
	"github.com/velocitykode/velocity/pkg/router"
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

// LoadTemplateFromFile loads the root template from a file
func LoadTemplateFromFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}
