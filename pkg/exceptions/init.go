package exceptions

import (
	"os"
	"strings"
	"sync"

	"github.com/joho/godotenv"
)

var (
	globalHandler *Handler
	initOnce      sync.Once
	mu            sync.RWMutex
)

// Initialize initializes the global exception handler.
// This is called automatically on first use, but can be called explicitly
// to configure the handler before use.
func Initialize(opts ...HandlerOption) {
	initOnce.Do(func() {
		_ = godotenv.Load()

		// Determine debug mode from environment
		debug := isDebugMode()
		env := getEnvOrDefault("APP_ENV", "production")
		apiMode := isAPIMode()

		// Create base options
		baseOpts := []HandlerOption{
			WithDebug(debug),
			WithEnvironment(env),
			WithAPIMode(apiMode),
			WithAPIPrefixes("/api"),
		}

		// Merge with provided options
		allOpts := append(baseOpts, opts...)

		handler := NewHandler(allOpts...)

		mu.Lock()
		globalHandler = handler
		mu.Unlock()
	})
}

// Get returns the global exception handler.
// If not initialized, it will be initialized with default settings.
func Get() *Handler {
	mu.RLock()
	h := globalHandler
	mu.RUnlock()

	if h != nil {
		return h
	}

	Initialize()

	mu.RLock()
	h = globalHandler
	mu.RUnlock()
	return h
}

// SetGlobal sets the global exception handler.
// This can be used to replace the default handler with a custom one.
func SetGlobal(handler *Handler) {
	mu.Lock()
	defer mu.Unlock()
	globalHandler = handler
}

// Report reports an exception using the global handler.
func Report(err error, ctx *ExceptionContext) {
	Get().Report(err, ctx)
}

// Render renders an exception using the global handler.
func Render(ctx RenderContext, err error, exCtx *ExceptionContext) {
	Get().Render(ctx, err, exCtx)
}

// Handle handles an exception using the global handler.
func Handle(ctx RenderContext, err error) {
	Get().Handle(ctx, err)
}

// HandleWithContext handles an exception with context using the global handler.
func HandleWithContext(ctx RenderContext, err error, exCtx *ExceptionContext) {
	Get().HandleWithContext(ctx, err, exCtx)
}

// IsDebug returns whether the global handler is in debug mode.
func IsDebug() bool {
	return Get().IsDebug()
}

// SetDebug sets the debug mode on the global handler.
func SetDebug(debug bool) {
	Get().SetDebug(debug)
}

// IsAPIMode returns whether API mode is enabled on the global handler.
func IsAPIMode() bool {
	return Get().IsAPIMode()
}

// SetAPIMode sets the API mode on the global handler.
func SetAPIMode(enabled bool) {
	Get().SetAPIMode(enabled)
}

// SetAPIPrefixes sets URL prefixes that indicate API routes on the global handler.
func SetAPIPrefixes(prefixes ...string) {
	Get().SetAPIPrefixes(prefixes...)
}

// isDebugMode determines if debug mode should be enabled.
func isDebugMode() bool {
	// Check APP_DEBUG environment variable
	debugStr := strings.ToLower(getEnvOrDefault("APP_DEBUG", "false"))
	if debugStr == "true" || debugStr == "1" || debugStr == "yes" {
		return true
	}

	// Also enable debug for local/development environments
	env := strings.ToLower(getEnvOrDefault("APP_ENV", "production"))
	return env == "local" || env == "development"
}

// isAPIMode determines if API mode should be enabled from environment.
func isAPIMode() bool {
	apiModeStr := strings.ToLower(getEnvOrDefault("API_MODE", "false"))
	return apiModeStr == "true" || apiModeStr == "1" || apiModeStr == "yes"
}

// getEnvOrDefault returns the environment variable value or the default.
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// ResetGlobal resets the global handler for testing purposes.
// This should only be used in tests.
func ResetGlobal() {
	mu.Lock()
	defer mu.Unlock()
	globalHandler = nil
	initOnce = sync.Once{}
}
