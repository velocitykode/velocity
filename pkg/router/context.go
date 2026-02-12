package router

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/velocitykode/velocity/pkg/app"
	"github.com/velocitykode/velocity/pkg/cache"
	"github.com/velocitykode/velocity/pkg/crypto"
	"github.com/velocitykode/velocity/pkg/events"
	"github.com/velocitykode/velocity/pkg/exceptions"
	"github.com/velocitykode/velocity/pkg/log"
	"github.com/velocitykode/velocity/pkg/mail"
	"github.com/velocitykode/velocity/pkg/orm"
	"github.com/velocitykode/velocity/pkg/queue"
	"github.com/velocitykode/velocity/pkg/scheduler"
	"github.com/velocitykode/velocity/pkg/storage"
	"github.com/velocitykode/velocity/pkg/validation"
)

// HandlerFunc is the Velocity handler function signature
type HandlerFunc func(c *Context) error

// MiddlewareFunc is the Velocity middleware function signature
type MiddlewareFunc func(next HandlerFunc) HandlerFunc

// Context wraps http.Request and http.ResponseWriter with helper methods
type Context struct {
	Response http.ResponseWriter
	Request  *http.Request
	params   map[string]string
	// For storing values across middleware
	values   map[string]interface{}
	services *app.Services
}

// NewContext creates a new Context from http.Request and http.ResponseWriter
func NewContext(w http.ResponseWriter, r *http.Request) *Context {
	return &Context{
		Response: w,
		Request:  r,
		params:   GetParams(r),
		values:   make(map[string]interface{}),
	}
}

// Set stores a value in the context
func (c *Context) Set(key string, value interface{}) {
	c.values[key] = value
}

// Get retrieves a value from the context
func (c *Context) Get(key string) interface{} {
	return c.values[key]
}

// GetString retrieves a string value from the context
func (c *Context) GetString(key string) string {
	if v, ok := c.values[key].(string); ok {
		return v
	}
	return ""
}

// Param returns a route parameter by name
func (c *Context) Param(name string) string {
	return c.params[name]
}

// ParamInt returns a route parameter as int
func (c *Context) ParamInt(name string) (int, error) {
	val := c.Param(name)
	if val == "" {
		return 0, fmt.Errorf("param '%s' not found", name)
	}
	return strconv.Atoi(val)
}

// ParamInt64 returns a route parameter as int64
func (c *Context) ParamInt64(name string) (int64, error) {
	val := c.Param(name)
	if val == "" {
		return 0, fmt.Errorf("param '%s' not found", name)
	}
	return strconv.ParseInt(val, 10, 64)
}

// Query returns a query parameter by name
func (c *Context) Query(name string) string {
	return c.Request.URL.Query().Get(name)
}

// QueryDefault returns a query parameter or default value if not set
func (c *Context) QueryDefault(name, defaultValue string) string {
	value := c.Query(name)
	if value == "" {
		return defaultValue
	}
	return value
}

// QueryInt returns a query parameter as int, with optional default
func (c *Context) QueryInt(name string, defaultValue ...int) int {
	val := c.Query(name)
	if val == "" {
		if len(defaultValue) > 0 {
			return defaultValue[0]
		}
		return 0
	}
	i, err := strconv.Atoi(val)
	if err != nil {
		if len(defaultValue) > 0 {
			return defaultValue[0]
		}
		return 0
	}
	return i
}

// QueryInt64 returns a query parameter as int64, with optional default
func (c *Context) QueryInt64(name string, defaultValue ...int64) int64 {
	val := c.Query(name)
	if val == "" {
		if len(defaultValue) > 0 {
			return defaultValue[0]
		}
		return 0
	}
	i, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		if len(defaultValue) > 0 {
			return defaultValue[0]
		}
		return 0
	}
	return i
}

// QueryFloat64 returns a query parameter as float64, with optional default
func (c *Context) QueryFloat64(name string, defaultValue ...float64) float64 {
	val := c.Query(name)
	if val == "" {
		if len(defaultValue) > 0 {
			return defaultValue[0]
		}
		return 0
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil && len(defaultValue) > 0 {
		return defaultValue[0]
	}
	return f
}

// QueryBool returns a query parameter as bool (handles "true", "1", "yes")
func (c *Context) QueryBool(name string) bool {
	val := strings.ToLower(c.Query(name))
	return val == "true" || val == "1" || val == "yes"
}

// Header returns a request header value
func (c *Context) Header(name string) string {
	return c.Request.Header.Get(name)
}

// HeaderInt64 returns a header value as int64, with optional default
func (c *Context) HeaderInt64(name string, defaultValue ...int64) int64 {
	val := c.Header(name)
	if val == "" {
		if len(defaultValue) > 0 {
			return defaultValue[0]
		}
		return 0
	}
	i, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		if len(defaultValue) > 0 {
			return defaultValue[0]
		}
		return 0
	}
	return i
}

// SetHeader sets a response header
func (c *Context) SetHeader(name, value string) {
	c.Response.Header().Set(name, value)
}

// Cookie returns a cookie by name
func (c *Context) Cookie(name string) (*http.Cookie, error) {
	return c.Request.Cookie(name)
}

// SetCookie sets a cookie on the response
func (c *Context) SetCookie(cookie *http.Cookie) {
	http.SetCookie(c.Response, cookie)
}

// JSON sends a JSON response with the given status code
func (c *Context) JSON(status int, data interface{}) error {
	c.Response.Header().Set("Content-Type", "application/json")
	c.Response.WriteHeader(status)
	return json.NewEncoder(c.Response).Encode(data)
}

// String sends a plain text response
func (c *Context) String(status int, text string) error {
	c.Response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.Response.WriteHeader(status)
	_, err := c.Response.Write([]byte(text))
	return err
}

// HTML sends an HTML response.
// WARNING: This method writes raw, unescaped HTML content. Callers must sanitize
// any user-supplied input before passing it to this method to prevent XSS attacks.
func (c *Context) HTML(status int, html string) error {
	c.Response.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Response.WriteHeader(status)
	_, err := c.Response.Write([]byte(html))
	return err
}

// Redirect redirects to a URL with the given status code.
// The URL is validated to prevent open redirects: only relative paths and
// same-host URLs are allowed. Absolute URLs to external domains redirect to "/".
func (c *Context) Redirect(status int, rawURL string) error {
	rawURL = sanitizeRedirectForHost(rawURL, c.Request.Host)
	http.Redirect(c.Response, c.Request, rawURL, status)
	return nil
}

// Status sends a response with just a status code
func (c *Context) Status(status int) error {
	c.Response.WriteHeader(status)
	return nil
}

// NoContent sends a 204 No Content response
func (c *Context) NoContent() error {
	c.Response.WriteHeader(http.StatusNoContent)
	return nil
}

// DefaultMaxBodySize is the default maximum request body size (10MB).
const DefaultMaxBodySize int64 = 10 * 1024 * 1024

// Bind parses the request body as JSON into the given struct
func (c *Context) Bind(v interface{}) error {
	c.Request.Body = http.MaxBytesReader(c.Response, c.Request.Body, DefaultMaxBodySize)
	return json.NewDecoder(c.Request.Body).Decode(v)
}

// Method returns the HTTP method
func (c *Context) Method() string {
	return c.Request.Method
}

// Path returns the request path
func (c *Context) Path() string {
	return c.Request.URL.Path
}

// IP returns the client IP address from RemoteAddr (with port stripped).
// This does NOT trust X-Forwarded-For or X-Real-IP by default to prevent
// IP spoofing. Use the rate limiter's WithTrustedProxies option for
// proxy-aware IP extraction.
func (c *Context) IP() string {
	addr := c.Request.RemoteAddr
	// Strip port
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// IsAjax returns true if the request is an AJAX request
func (c *Context) IsAjax() bool {
	return c.Request.Header.Get("X-Requested-With") == "XMLHttpRequest"
}

// WantsJSON returns true if the client expects a JSON response
func (c *Context) WantsJSON() bool {
	accept := c.Request.Header.Get("Accept")
	return accept == "application/json" || c.Request.Header.Get("X-Inertia") != ""
}

// Wrap converts a HandlerFunc to http.HandlerFunc
func Wrap(h HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := NewContext(w, r)
		if err := h(c); err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}
}

// Error represents an HTTP error response
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error sends a JSON error response
func (c *Context) Error(status int, message string) error {
	return c.JSON(status, Error{
		Code:    status,
		Message: message,
	})
}

// NotFound sends a 404 error response
func (c *Context) NotFound(message ...string) error {
	msg := "Not Found"
	if len(message) > 0 {
		msg = message[0]
	}
	return c.Error(http.StatusNotFound, msg)
}

// BadRequest sends a 400 error response
func (c *Context) BadRequest(message ...string) error {
	msg := "Bad Request"
	if len(message) > 0 {
		msg = message[0]
	}
	return c.Error(http.StatusBadRequest, msg)
}

// Unauthorized sends a 401 error response
func (c *Context) Unauthorized(message ...string) error {
	msg := "Unauthorized"
	if len(message) > 0 {
		msg = message[0]
	}
	return c.Error(http.StatusUnauthorized, msg)
}

// sanitizeRedirectForHost validates a redirect URL to prevent open redirects.
// Allows relative paths and same-host URLs. Rejects absolute URLs to external domains.
func sanitizeRedirectForHost(target, host string) string {
	// Allow relative paths (but not protocol-relative //evil.com)
	if strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "//") {
		return target
	}
	u, err := url.Parse(target)
	if err != nil {
		return "/"
	}
	if u.Host != "" && u.Host != host {
		return "/"
	}
	return target
}

// Forbidden sends a 403 error response
func (c *Context) Forbidden(message ...string) error {
	msg := "Forbidden"
	if len(message) > 0 {
		msg = message[0]
	}
	return c.Error(http.StatusForbidden, msg)
}

// SetServices sets the service container on this context.
func (c *Context) SetServices(s *app.Services) {
	c.services = s
}

// mustServices returns the service container or panics if it is nil.
func (c *Context) mustServices() *app.Services {
	if c.services == nil {
		panic("velocity: router.Context has no services — create the router via velocity.New()")
	}
	return c.services
}

// Services returns the service container.
func (c *Context) Services() *app.Services {
	return c.mustServices()
}

// DB returns the ORM manager.
func (c *Context) DB() *orm.Manager {
	return c.mustServices().DB
}

// Cache returns the cache manager.
func (c *Context) Cache() *cache.Manager {
	return c.mustServices().Cache
}

// Log returns the logger.
func (c *Context) Log() log.Logger {
	return c.mustServices().Log
}

// Queue returns the queue driver.
func (c *Context) Queue() queue.Driver {
	return c.mustServices().Queue
}

// Storage returns the storage manager.
func (c *Context) Storage() *storage.Manager {
	return c.mustServices().Storage
}

// Mail returns the mailer.
func (c *Context) Mail() mail.Mailer {
	return c.mustServices().Mail
}

// Events returns the event dispatcher.
func (c *Context) Events() events.Dispatcher {
	return c.mustServices().Events
}

// Crypto returns the encryptor.
func (c *Context) Crypto() crypto.Encryptor {
	return c.mustServices().Crypto
}

// Validator returns the validator.
func (c *Context) Validator() validation.Validator {
	return c.mustServices().Validator
}

// Exceptions returns the exception handler.
func (c *Context) Exceptions() *exceptions.Handler {
	return c.mustServices().Exceptions
}

// Scheduler returns the task scheduler.
func (c *Context) Scheduler() *scheduler.Scheduler {
	return c.mustServices().Scheduler
}

// Auth returns the auth manager (*auth.Manager). Requires type assertion.
func (c *Context) Auth() any {
	return c.mustServices().Auth
}

// CSRF returns the CSRF protection instance (*csrf.CSRF). Requires type assertion.
func (c *Context) CSRF() any {
	return c.mustServices().CSRF
}

// View returns the view engine (*view.Engine). Requires type assertion.
func (c *Context) View() any {
	return c.mustServices().View
}
