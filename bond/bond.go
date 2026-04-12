package bond

import (
	"errors"
	"html/template"
	"net/http"
	"sync"
	"time"
)

// Errors
var (
	ErrNotInitialized   = errors.New("bond: not initialized")
	ErrInvalidTemplate  = errors.New("bond: invalid template - must contain {{ .inertia }}")
	ErrTemplateRequired = errors.New("bond: template is required")
)

// Config holds bond configuration
type Config struct {
	RootTemplate   string // Full HTML template with {{ .inertia }} placeholder
	Version        string // Asset version for cache busting
	ContainerID    string // Default: "app"
	EncryptHistory bool   // Use pkg/crypto for history state encryption
	SSR            SSRConfig
}

// SSRConfig configures server-side rendering. When Enabled is false
// (the default) bond renders pages as plain CSR — the Inertia container
// ships with an empty inner body and the client hydrates the page data
// from the data-page attribute.
type SSRConfig struct {
	Enabled bool
	URL     string        // Defaults to DefaultSSRURL when empty
	Timeout time.Duration // Defaults to 3s
	Except  []string      // URL prefixes excluded from SSR
}

// Bond is the main Inertia handler
type Bond struct {
	mu             sync.RWMutex
	template       *template.Template
	version        string
	containerID    string
	encryptHistory bool
	encryptor      interface {
		Encrypt(string) (string, error)
		Decrypt(string) (string, error)
	}

	// Shared props
	sharedProps    Props
	sharedFuncs    map[string]SharedPropFunc
	sharePropsFunc func(r *http.Request) (Props, error) // Per-request props function

	// Server-side rendering. When nil, renderHTML emits the standard
	// CSR container. SetSSRGateway is safe to call after New.
	ssr SSRGateway
}

// SetEncryptor sets the encryptor used for history state encryption.
func (b *Bond) SetEncryptor(enc interface {
	Encrypt(string) (string, error)
	Decrypt(string) (string, error)
}) {
	b.encryptor = enc
}

// New creates a new Bond instance
func New(config Config) (*Bond, error) {
	if config.RootTemplate == "" {
		return nil, ErrTemplateRequired
	}

	// Parse the template
	tmpl, err := parseTemplate(config.RootTemplate)
	if err != nil {
		return nil, err
	}

	// Set defaults
	containerID := config.ContainerID
	if containerID == "" {
		containerID = "app"
	}

	version := config.Version
	if version == "" {
		version = "1"
	}

	b := &Bond{
		template:       tmpl,
		version:        version,
		containerID:    containerID,
		encryptHistory: config.EncryptHistory,
		sharedProps:    make(Props),
		sharedFuncs:    make(map[string]SharedPropFunc),
	}

	if config.SSR.Enabled {
		gw := NewHTTPGateway(config.SSR.URL)
		if config.SSR.Timeout > 0 {
			gw.Timeout = config.SSR.Timeout
			gw.Client.Timeout = config.SSR.Timeout
		}
		if len(config.SSR.Except) > 0 {
			gw.Except = config.SSR.Except
		}
		b.ssr = gw
	}

	return b, nil
}

// SetSSRGateway overrides the SSR gateway used for server-side rendering.
// Pass nil to disable SSR. Intended for tests and custom gateways.
func (b *Bond) SetSSRGateway(gw SSRGateway) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ssr = gw
}

// Version returns the configured asset version
func (b *Bond) Version() string {
	return b.version
}

// ContainerID returns the configured container ID
func (b *Bond) ContainerID() string {
	return b.containerID
}

// isInertiaRequest checks if the request is an Inertia XHR request
func isInertiaRequest(r *http.Request) bool {
	return r.Header.Get("X-Inertia") == "true"
}

// SetSharePropsFunc sets a function that returns props to be shared per request.
func (b *Bond) SetSharePropsFunc(fn func(r *http.Request) (Props, error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sharePropsFunc = fn
}
