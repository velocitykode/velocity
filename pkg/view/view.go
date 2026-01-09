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

// Initialize sets up the view system using Bond
func Initialize(config Config) error {
	// Set default template if not provided
	if config.RootTemplate == "" {
		config.RootTemplate = defaultTemplate
	}

	// Set default version if not provided
	if config.Version == "" {
		config.Version = "1"
	}

	return bond.Initialize(bond.Config{
		RootTemplate: config.RootTemplate,
		Version:      config.Version,
		ContainerID:  "app",
	})
}

// Render renders a component with optional props
func Render(w http.ResponseWriter, r *http.Request, component string, props ...Props) error {
	var p Props
	if len(props) > 0 && props[0] != nil {
		p = props[0]
	}
	return bond.Render(w, r, component, p)
}

// Share shares a prop globally
func Share(key string, value interface{}) {
	bond.Share(key, value)
}

// ShareFunc shares a dynamic prop evaluated per-request
func ShareFunc(key string, fn func(r *http.Request) (interface{}, error)) {
	bond.ShareFunc(key, func(r *http.Request) (any, error) {
		return fn(r)
	})
}

// ShareMultiple shares multiple props at once
func ShareMultiple(props Props) {
	bond.ShareMultiple(props)
}

// SetSharePropsFunc sets a function that will be called to get shared props for each request
func SetSharePropsFunc(fn SharePropsFunc) {
	bond.SetSharePropsFunc(fn)
}

// Redirect performs an SPA redirect (for internal navigation)
// Uses 303 See Other for POST-Redirect-GET pattern
func Redirect(w http.ResponseWriter, r *http.Request, url string) {
	bond.Redirect(w, r, url)
}

// Location performs an external redirect (forces full page load)
// Use for external URLs or when you need to break out of the SPA
func Location(w http.ResponseWriter, r *http.Request, url string) {
	bond.Location(w, r, url)
}

// Back redirects back (SPA navigation)
func Back(w http.ResponseWriter, r *http.Request) {
	bond.Back(w, r)
}

// Middleware returns the Inertia middleware
func Middleware() router.MiddlewareFunc {
	return bond.Middleware()
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
