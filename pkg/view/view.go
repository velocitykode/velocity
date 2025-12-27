package view

import (
	"context"
	"net/http"
	"os"
	"sync"

	"github.com/romsar/gonertia"
	"github.com/velocitykode/velocity/pkg/router"
)

// Manager manages view engines
type Manager struct {
	mu             sync.RWMutex
	inertia        *gonertia.Inertia
	template       string
	version        string
	sharePropsFunc SharePropsFunc
}

var (
	instance *Manager
	once     sync.Once
)

// Config holds view configuration
type Config struct {
	RootTemplate string
	Version      string
	SSREnabled   bool
	SSRURL       string
}

// Initialize sets up the view manager with gonertia
func Initialize(config Config) error {
	var initErr error
	once.Do(func() {
		// Set default template if not provided
		if config.RootTemplate == "" {
			config.RootTemplate = defaultTemplate
		}

		// Set default version if not provided
		if config.Version == "" {
			config.Version = "1"
		}

		// Create gonertia instance
		inertiaInstance, err := gonertia.New(
			config.RootTemplate,
			gonertia.WithVersion(config.Version),
			gonertia.WithContainerID("app"),
		)
		if err != nil {
			initErr = err
			return
		}

		instance = &Manager{
			inertia:  inertiaInstance,
			template: config.RootTemplate,
			version:  config.Version,
		}
	})

	return initErr
}

// Get returns the singleton view manager
func Get() *Manager {
	if instance == nil {
		// Auto-initialize with defaults if not initialized
		Initialize(Config{
			RootTemplate: defaultTemplate,
			Version:      "1",
		})
	}
	return instance
}

// Inertia returns the gonertia instance
func Inertia() *gonertia.Inertia {
	return Get().inertia
}

// Props is a type alias for gonertia.Props
type Props = gonertia.Props

// SetProps sets props in the request context for sharing across the request
func SetProps(ctx context.Context, props Props) context.Context {
	return gonertia.SetProps(ctx, props)
}

// SetProp sets a single prop in the request context
func SetProp(ctx context.Context, key string, value interface{}) context.Context {
	return gonertia.SetProp(ctx, key, value)
}

// SharePropsFunc is a function that returns props to be shared
type SharePropsFunc func(r *http.Request) (Props, error)

// SetSharePropsFunc sets a function that will be called to get shared props for each request
func SetSharePropsFunc(fn SharePropsFunc) {
	manager := Get()
	manager.mu.Lock()
	manager.sharePropsFunc = fn
	manager.mu.Unlock()
}

// Render renders a component with props
// It automatically merges props from context (set by middleware) with the provided props
func Render(w http.ResponseWriter, r *http.Request, component string, props Props) error {
	manager := Get()

	// If there's a sharePropsFunc, call it and merge the props
	if manager.sharePropsFunc != nil {
		sharedProps, err := manager.sharePropsFunc(r)
		if err == nil && sharedProps != nil {
			// Merge shared props with component props
			mergedProps := make(Props)
			for k, v := range sharedProps {
				mergedProps[k] = v
			}
			for k, v := range props {
				mergedProps[k] = v
			}
			props = mergedProps
		}
	}

	return manager.inertia.Render(w, r, component, props)
}

// Share shares props globally
func Share(key string, value interface{}) {
	Get().inertia.ShareProp(key, value)
}

// ShareFunc shares a function prop
func ShareFunc(key string, fn func(r *http.Request) (interface{}, error)) {
	// Note: gonertia doesn't have ShareFunc for props, only for template funcs
	// We'll need to handle this differently
	Get().inertia.ShareProp(key, fn)
}

// ShareMultiple shares multiple props at once
func ShareMultiple(props gonertia.Props) {
	for key, value := range props {
		Get().inertia.ShareProp(key, value)
	}
}

// Redirect performs a plain Inertia redirect (for internal navigation)
// This is the correct method for SPA navigation within your app
// Uses 303 See Other for POST-Redirect-GET pattern
func Redirect(w http.ResponseWriter, r *http.Request, url string) {
	Get().inertia.Redirect(w, r, url, 303)
}

// Location performs an external redirect (forces full page load)
// Use this only for external URLs or when you need to break out of the SPA
func Location(w http.ResponseWriter, r *http.Request, url string) {
	Get().inertia.Location(w, r, url)
}

// Back redirects back (SPA navigation)
func Back(w http.ResponseWriter, r *http.Request) {
	referer := r.Header.Get("Referer")
	if referer == "" {
		referer = "/"
	}
	http.Redirect(w, r, referer, http.StatusSeeOther)
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

// Note: FlashProvider and validation providers should be set during initialization
// using gonertia.WithFlashProvider() option

// Middleware returns gonertia middleware for use with routers
func Middleware() router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			Get().inertia.Middleware(router.Wrap(next)).ServeHTTP(c.Response, c.Request)
			return nil
		}
	}
}

// WithContext returns an Inertia instance from the request context
func WithContext(r *http.Request) *gonertia.Inertia {
	// Note: gonertia doesn't store itself in context
	return Get().inertia
}

// SetTemplateData sets template data for the root template
func SetTemplateData(data gonertia.TemplateData) {
	// Template data needs to be set during render or initialization
	for k, v := range data {
		Get().inertia.ShareTemplateData(k, v)
	}
}

// ShareTemplateData shares data to the template
func ShareTemplateData(key string, value interface{}) {
	Get().inertia.ShareTemplateData(key, value)
}

// ShareTemplateFunc shares a function to the template
func ShareTemplateFunc(key string, fn interface{}) error {
	return Get().inertia.ShareTemplateFunc(key, fn)
}
