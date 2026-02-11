package bond

import (
	"errors"
	"html/template"
	"net/http"
	"sync"

	"github.com/velocitykode/velocity/pkg/router"
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
}

// Bond is the main Inertia handler
type Bond struct {
	mu             sync.RWMutex
	template       *template.Template
	version        string
	containerID    string
	encryptHistory bool

	// Shared props
	sharedProps    Props
	sharedFuncs    map[string]SharedPropFunc
	sharePropsFunc func(r *http.Request) (Props, error) // Per-request props function
}

// Global instance
var (
	instance *Bond
	initOnce sync.Once
	initMu   sync.RWMutex
)

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

	return &Bond{
		template:       tmpl,
		version:        version,
		containerID:    containerID,
		encryptHistory: config.EncryptHistory,
		sharedProps:    make(Props),
		sharedFuncs:    make(map[string]SharedPropFunc),
	}, nil
}

func Initialize(config Config) error {
	initMu.Lock()
	defer initMu.Unlock()

	b, err := New(config)
	if err != nil {
		return err
	}

	instance = b
	return nil
}

func Get() *Bond {
	initMu.RLock()
	defer initMu.RUnlock()

	if instance == nil {
		panic(ErrNotInitialized)
	}
	return instance
}

// Version returns the configured asset version
func (b *Bond) Version() string {
	return b.version
}

// ContainerID returns the configured container ID
func (b *Bond) ContainerID() string {
	return b.containerID
}

// resetGlobal resets the global instance (for testing)
func resetGlobal() {
	initMu.Lock()
	defer initMu.Unlock()
	instance = nil
	initOnce = sync.Once{}
}

// ResetForTesting resets the global instance - exported for use by other packages in tests
func ResetForTesting() {
	resetGlobal()
}

// isInertiaRequest checks if the request is an Inertia XHR request
func isInertiaRequest(r *http.Request) bool {
	return r.Header.Get("X-Inertia") == "true"
}

// --- Package-level convenience functions using global instance ---

func Render(w http.ResponseWriter, r *http.Request, component string, props Props) error {
	return Get().Render(w, r, component, props)
}

func Share(key string, value any) {
	Get().Share(key, value)
}

func ShareFunc(key string, fn SharedPropFunc) {
	Get().ShareFunc(key, fn)
}

func ShareMultiple(props Props) {
	Get().ShareMultiple(props)
}

func Redirect(w http.ResponseWriter, r *http.Request, url string) {
	Get().Redirect(w, r, url)
}

func Location(w http.ResponseWriter, r *http.Request, url string) {
	Get().Location(w, r, url)
}

func Back(w http.ResponseWriter, r *http.Request) {
	Get().Back(w, r)
}

func Middleware() router.MiddlewareFunc {
	return Get().MiddlewareFunc()
}

func SetSharePropsFunc(fn func(r *http.Request) (Props, error)) {
	Get().SetSharePropsFunc(fn)
}

// SetSharePropsFunc sets a function that returns props to be shared per request.
func (b *Bond) SetSharePropsFunc(fn func(r *http.Request) (Props, error)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sharePropsFunc = fn
}
