package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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
	values map[string]interface{}
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

// HTML sends an HTML response
func (c *Context) HTML(status int, html string) error {
	c.Response.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Response.WriteHeader(status)
	_, err := c.Response.Write([]byte(html))
	return err
}

// Redirect redirects to a URL with the given status code
func (c *Context) Redirect(status int, url string) error {
	http.Redirect(c.Response, c.Request, url, status)
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

// Bind parses the request body as JSON into the given struct
func (c *Context) Bind(v interface{}) error {
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

// IP returns the client IP address
func (c *Context) IP() string {
	// Check X-Forwarded-For first
	if xff := c.Request.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	// Check X-Real-IP
	if xri := c.Request.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	return c.Request.RemoteAddr
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
			// Default error handling - can be customized
			http.Error(w, err.Error(), http.StatusInternalServerError)
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

// Forbidden sends a 403 error response
func (c *Context) Forbidden(message ...string) error {
	msg := "Forbidden"
	if len(message) > 0 {
		msg = message[0]
	}
	return c.Error(http.StatusForbidden, msg)
}
