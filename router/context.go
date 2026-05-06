package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/exceptions"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/notification"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/resource"
	"github.com/velocitykode/velocity/scheduler"
	"github.com/velocitykode/velocity/storage"
	"github.com/velocitykode/velocity/validation"
)

// HandlerFunc is the Velocity handler function signature
type HandlerFunc func(c *Context) error

// MiddlewareFunc is the Velocity middleware function signature
type MiddlewareFunc func(next HandlerFunc) HandlerFunc

// servicesCtxKey is used to propagate *app.Services through r.Context()
// so that Wrap / NewContext automatically inherit them.
type servicesCtxKey struct{}

// WithServices returns a copy of r whose context carries s.
func WithServices(r *http.Request, s *app.Services) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), servicesCtxKey{}, s))
}

// Param represents a single route parameter key-value pair.
type RouteParam struct {
	Key   string
	Value string
}

// Context wraps http.Request and http.ResponseWriter with helper methods
type Context struct {
	Response http.ResponseWriter
	Request  *http.Request
	params   []RouteParam
	// For storing values across middleware
	values     map[string]interface{}
	services   *app.Services
	sseStarted bool
	// trustedProxies is the parsed, immutable set of trusted proxy
	// networks carried over from the router. May be nil when unset.
	trustedProxies *TrustedProxies
	// redirectAllowedHosts, when non-empty, extends same-origin redirect
	// validation to this explicit allowlist. It is taken from the router
	// when the context is acquired from the pool.
	redirectAllowedHosts []string
	validateFn           func(c *Context, rules map[string][]string, messages ...map[string]string) error
}

// NewContext creates a new Context from http.Request and http.ResponseWriter.
// If services were previously stashed on r.Context() (via the router pipeline),
// they are inherited automatically.
func NewContext(w http.ResponseWriter, r *http.Request) *Context {
	var svc *app.Services
	if s, ok := r.Context().Value(servicesCtxKey{}).(*app.Services); ok {
		svc = s
	}
	// Convert map params from request context to []RouteParam
	mapParams := GetParams(r)
	var params []RouteParam
	if len(mapParams) > 0 {
		params = make([]RouteParam, 0, len(mapParams))
		for k, v := range mapParams {
			params = append(params, RouteParam{Key: k, Value: v})
		}
	}
	return &Context{
		Response: w,
		Request:  r,
		params:   params,
		values:   make(map[string]interface{}),
		services: svc,
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
	for _, p := range c.params {
		if p.Key == name {
			return p.Value
		}
	}
	return ""
}

// ErrParamNotFound indicates a route parameter by the given name is
// not present on the context. Typically a programming error, the
// handler referenced a name the route pattern does not declare.
var ErrParamNotFound = errors.New("velocity/router: route param not found")

// ErrParamParse indicates a route parameter existed on the context
// but failed numeric parsing. Typically a client-supplied value that
// does not fit the expected shape; the handler should map this to a
// 400 Bad Request.
var ErrParamParse = errors.New("velocity/router: route param parse error")

// ParamInt returns a route parameter as int.
//
// Returns ErrParamNotFound (wrapped) when the parameter is missing,
// or ErrParamParse (wrapped) when the parameter is present but not a
// valid base-10 integer. The two sentinels let callers distinguish
// a programming error from malformed input without string-matching.
func (c *Context) ParamInt(name string) (int, error) {
	val := c.Param(name)
	if val == "" {
		return 0, fmt.Errorf("%w: %q", ErrParamNotFound, name)
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("%w: %q=%q: %v", ErrParamParse, name, val, err)
	}
	return n, nil
}

// ParamInt64 returns a route parameter as int64.
//
// Returns ErrParamNotFound (wrapped) when the parameter is missing,
// or ErrParamParse (wrapped) when the parameter is present but not a
// valid base-10 signed 64-bit integer.
func (c *Context) ParamInt64(name string) (int64, error) {
	val := c.Param(name)
	if val == "" {
		return 0, fmt.Errorf("%w: %q", ErrParamNotFound, name)
	}
	n, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q=%q: %v", ErrParamParse, name, val, err)
	}
	return n, nil
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

// QueryBool returns a query parameter as bool.
// Uses strconv.ParseBool, which accepts "1", "t", "T", "TRUE", "true", "True",
// "0", "f", "F", "FALSE", "false", "False". Returns false for unrecognized values.
func (c *Context) QueryBool(name string) bool {
	b, _ := strconv.ParseBool(c.Query(name))
	return b
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

// SetHeader sets a response header.
// Names and values containing \r or \n are rejected to prevent header injection.
func (c *Context) SetHeader(name, value string) {
	if strings.ContainsAny(name, "\r\n") || strings.ContainsAny(value, "\r\n") {
		return
	}
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

// Resource transforms a Resource into JSON and sends it with a 200 status.
func (c *Context) Resource(r resource.Resource) error {
	return c.JSON(http.StatusOK, r.ToResource())
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
// hosts explicitly listed in Router.RedirectAllowedHosts are permitted.
// Any other absolute URL is rewritten to "/".
//
// Defaulting to an empty allowlist (i.e. reject every cross-host target)
// removes the footgun where a spoofed Host header tricked the router
// into treating an attacker-supplied destination as "same-origin".
func (c *Context) Redirect(status int, rawURL string) error {
	rawURL = sanitizeRedirect(rawURL, c.redirectAllowedHosts)
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
	if c.Get(bodyLimitKey) == nil {
		c.Request.Body = http.MaxBytesReader(c.Response, c.Request.Body, DefaultMaxBodySize)
	}
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
// If trusted proxies are configured and the remote address is trusted,
// X-Forwarded-For is consulted and the right-most untrusted hop is
// returned per RFC 7239.
func (c *Context) IP() string {
	host := stripPortHost(c.Request.RemoteAddr)
	if c.trustedProxies == nil || c.trustedProxies.Len() == 0 {
		return host
	}
	xff := c.Request.Header.Get("X-Forwarded-For")
	return c.trustedProxies.ClientIP(host, xff)
}

// reset clears the context for reuse by the sync.Pool.
func (c *Context) reset() {
	c.Response = nil
	c.Request = nil
	c.params = c.params[:0]
	for k := range c.values {
		delete(c.values, k)
	}
	c.services = nil
	c.sseStarted = false
	c.trustedProxies = nil
	c.redirectAllowedHosts = nil
	c.validateFn = nil
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

// httpError is a shared helper for error response methods.
func (c *Context) httpError(status int, defaultMsg string, message []string) error {
	msg := defaultMsg
	if len(message) > 0 {
		msg = message[0]
	}
	return c.Error(status, msg)
}

// NotFound sends a 404 error response
func (c *Context) NotFound(message ...string) error {
	return c.httpError(http.StatusNotFound, "Not Found", message)
}

// BadRequest sends a 400 error response
func (c *Context) BadRequest(message ...string) error {
	return c.httpError(http.StatusBadRequest, "Bad Request", message)
}

// Unauthorized sends a 401 error response
func (c *Context) Unauthorized(message ...string) error {
	return c.httpError(http.StatusUnauthorized, "Unauthorized", message)
}

// sanitizeRedirect validates a redirect URL against an explicit host
// allowlist. Relative paths ("/foo", but not "//evil.com") are always
// accepted. Absolute URLs are accepted only when the host matches one
// of allowedHosts. Everything else is rewritten to "/".
//
// Subtle cases that FuzzSanitizeRedirect surfaced and this function must
// keep rejecting:
//   - "//evil", "///evil": protocol-relative URLs parse with empty Host
//     in url.URL, so a bare Host!="" check wasn't enough.
//   - "javascript:...", "data:...": opaque-scheme URLs have Host=="" but
//     Scheme!="" and are live XSS vectors; they must be stripped.
//   - "https://trusted@evil": url.Parse puts "evil" in Host, so the
//     allowlist check catches this naturally.
func sanitizeRedirect(target string, allowedHosts []string) string {
	if target == "" {
		return "/"
	}
	// Protocol-relative URLs (//evil.com, ///evil) are unsafe even though
	// they lack an explicit scheme, the browser resolves them against
	// the current page's scheme and ends up on attacker-controlled host.
	if strings.HasPrefix(target, "//") {
		return "/"
	}
	if strings.HasPrefix(target, "/") {
		return target
	}
	u, err := url.Parse(target)
	if err != nil {
		return "/"
	}
	if u.Host != "" {
		for _, allowed := range allowedHosts {
			if allowed != "" && u.Host == allowed {
				return target
			}
		}
		return "/"
	}
	// Scheme without host, javascript:, data:, file:, etc. All unsafe.
	if u.Scheme != "" {
		return "/"
	}
	// Schemeless, hostless: a bare relative reference like "foo.html".
	// Same-origin by definition.
	return target
}

// Forbidden sends a 403 error response
func (c *Context) Forbidden(message ...string) error {
	return c.httpError(http.StatusForbidden, "Forbidden", message)
}

// SetServices sets the service container on this context and stashes it
// on r.Context() so that any downstream Wrap / NewContext inherits it.
func (c *Context) SetServices(s *app.Services) {
	c.services = s
	if s != nil {
		c.Request = WithServices(c.Request, s)
	}
}

// mustServices returns the service container or panics if it is nil.
func (c *Context) mustServices() *app.Services {
	if c.services == nil {
		panic("velocity: router.Context has no services, create the router via velocity.New()")
	}
	return c.services
}

// requireService panics if the given service is nil (typed as any so it works
// with both interface and pointer fields). The returned value is the same svc;
// callers must type-assert.
func requireService(c *Context, svc any, name string) {
	if svc == nil {
		panic(fmt.Sprintf("velocity: %s service not configured", name))
	}
}

// Services returns the service container.
func (c *Context) Services() *app.Services {
	return c.mustServices()
}

// ServicesIfSet returns the service container without panicking. Returns nil
// when services have not been wired (e.g. raw NewContext in unit tests, or
// helpers running before velocity.New() injects them). Use this from utility
// packages that want to degrade gracefully instead of crashing the request.
func (c *Context) ServicesIfSet() *app.Services {
	return c.services
}

// DB returns the ORM database interface.
func (c *Context) DB() orm.Database {
	s := c.mustServices()
	requireService(c, s.DB, "database")
	return s.DB
}

// Cache returns the cache manager interface.
func (c *Context) Cache() cache.CacheManager {
	s := c.mustServices()
	requireService(c, s.Cache, "cache")
	return s.Cache
}

// Log returns the logger.
func (c *Context) Log() log.Logger {
	s := c.mustServices()
	requireService(c, s.Log, "log")
	return s.Log
}

// Queue returns the queue driver.
func (c *Context) Queue() queue.Driver {
	s := c.mustServices()
	requireService(c, s.Queue, "queue")
	return s.Queue
}

// Storage returns the storage manager interface.
func (c *Context) Storage() storage.StorageManager {
	s := c.mustServices()
	requireService(c, s.Storage, "storage")
	return s.Storage
}

// Mail returns the mailer.
func (c *Context) Mail() mail.Mailer {
	s := c.mustServices()
	requireService(c, s.Mail, "mail")
	return s.Mail
}

// Notification returns the notification interface.
func (c *Context) Notification() notification.Notifier {
	s := c.mustServices()
	requireService(c, s.Notification, "notification")
	return s.Notification
}

// Events returns the event dispatcher.
func (c *Context) Events() events.Dispatcher {
	s := c.mustServices()
	requireService(c, s.Events, "events")
	return s.Events
}

// Crypto returns the encryptor.
func (c *Context) Crypto() crypto.Encryptor {
	s := c.mustServices()
	requireService(c, s.Crypto, "crypto")
	return s.Crypto
}

// Validator returns the validator.
func (c *Context) Validator() validation.Validator {
	s := c.mustServices()
	requireService(c, s.Validator, "validator")
	return s.Validator
}

// Exceptions returns the exception handler interface.
func (c *Context) Exceptions() exceptions.ExceptionHandler {
	s := c.mustServices()
	requireService(c, s.Exceptions, "exceptions")
	return s.Exceptions
}

// Scheduler returns the task scheduler interface.
func (c *Context) Scheduler() scheduler.TaskScheduler {
	s := c.mustServices()
	requireService(c, s.Scheduler, "scheduler")
	return s.Scheduler
}

// Auth returns the auth manager (*auth.Manager) as a contract.AuthManager.
func (c *Context) Auth() contract.AuthManager {
	s := c.mustServices()
	requireService(c, s.Auth, "auth")
	return s.Auth
}

// CSRF returns the CSRF protection instance (*csrf.CSRF) as a contract.CSRFProtector.
func (c *Context) CSRF() contract.CSRFProtector {
	s := c.mustServices()
	requireService(c, s.CSRF, "csrf")
	return s.CSRF
}

// View returns the view engine (*view.Engine) as a contract.ViewEngine.
func (c *Context) View() contract.ViewEngine {
	s := c.mustServices()
	requireService(c, s.View, "view")
	return s.View
}

// Can returns true if the authenticated user is allowed to perform the given
// ability. Returns false when auth is not configured or no user is logged in.
func (c *Context) Can(ability string, args ...interface{}) bool {
	if c.services == nil || c.services.Auth == nil {
		return false
	}
	return c.services.Auth.GateAllows(c.Request, ability, args...)
}

// Cannot returns true if the authenticated user is NOT allowed to perform the
// given ability. Returns true when auth is not configured.
func (c *Context) Cannot(ability string, args ...interface{}) bool {
	return !c.Can(ability, args...)
}

// Authorize checks if the authenticated user can perform the given ability and
// returns *HTTPError{403} if denied or auth is not configured. The returned
// error type is always *HTTPError so callers and error handlers can rely on it.
func (c *Context) Authorize(ability string, args ...interface{}) error {
	if c.services == nil || c.services.Auth == nil {
		return NewHTTPError(http.StatusForbidden)
	}
	if err := c.services.Auth.GateAuthorize(c.Request, ability, args...); err != nil {
		return NewHTTPError(http.StatusForbidden)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Multi-format binding
// ---------------------------------------------------------------------------

// BindForm parses form data and maps it to v using `form` struct tags.
func (c *Context) BindForm(v interface{}) error {
	limit := DefaultMaxBodySize
	if l, ok := c.Get(bodyLimitKey).(int64); ok {
		limit = l
	}
	c.Request.Body = http.MaxBytesReader(c.Response, c.Request.Body, limit)
	if err := c.Request.ParseForm(); err != nil {
		return err
	}
	return bindValues(v, c.Request.Form, "form")
}

// BindQuery maps URL query parameters to v using `query` struct tags.
func (c *Context) BindQuery(v interface{}) error {
	return bindValues(v, c.Request.URL.Query(), "query")
}

// BindXML parses the request body as XML into v (10 MB limit).
func (c *Context) BindXML(v interface{}) error {
	if c.Get(bodyLimitKey) == nil {
		c.Request.Body = http.MaxBytesReader(c.Response, c.Request.Body, DefaultMaxBodySize)
	}
	return xml.NewDecoder(c.Request.Body).Decode(v)
}

// BindAuto inspects Content-Type and delegates to the appropriate binder.
// Fallback is JSON.
func (c *Context) BindAuto(v interface{}) error {
	ct := c.Request.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/xml"), strings.HasPrefix(ct, "text/xml"):
		return c.BindXML(v)
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"),
		strings.HasPrefix(ct, "multipart/form-data"):
		return c.BindForm(v)
	default:
		return c.Bind(v)
	}
}

// Validatable is implemented by structs that define their own validation rules.
type Validatable interface {
	ValidationRules() validation.Rules
}

// BindValid binds JSON then validates using the struct's own rules (if any).
// Panics if the validator service is not configured.
func (c *Context) BindValid(v interface{}) error {
	if err := c.Bind(v); err != nil {
		return err
	}
	validator := c.Validator()
	if val, ok := v.(Validatable); ok {
		dataMap := structToMap(v)
		_, err := validator.Validate(dataMap, val.ValidationRules())
		return err
	}
	return nil
}

// structToMap converts a struct (or pointer to struct) to map[string]interface{}
// using json tags for field names.
func structToMap(v interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return result
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if !field.IsExported() {
			continue
		}
		name := field.Tag.Get("json")
		if name == "" || name == "-" {
			name = field.Name
		}
		if idx := strings.Index(name, ","); idx != -1 {
			name = name[:idx]
		}
		result[name] = rv.Field(i).Interface()
	}
	return result
}

// bindValues uses reflection to populate v from url.Values using the given struct tag.
func bindValues(v interface{}, vals url.Values, tag string) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return fmt.Errorf("bind target must be a non-nil pointer to struct")
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return fmt.Errorf("bind target must be a pointer to struct")
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		name := field.Tag.Get(tag)
		if name == "" || name == "-" {
			continue
		}
		val := vals.Get(name)
		if val == "" {
			continue
		}
		fv := rv.Field(i)
		if !fv.CanSet() {
			continue
		}
		switch fv.Kind() {
		case reflect.String:
			fv.SetString(val)
		case reflect.Int, reflect.Int64:
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return fmt.Errorf("field %s: %w", field.Name, err)
			}
			fv.SetInt(n)
		case reflect.Float64:
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return fmt.Errorf("field %s: %w", field.Name, err)
			}
			fv.SetFloat(f)
		case reflect.Bool:
			b, err := strconv.ParseBool(val)
			if err != nil {
				return fmt.Errorf("field %s: %w", field.Name, err)
			}
			fv.SetBool(b)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// XML response
// ---------------------------------------------------------------------------

// XML sends an XML response with the given status code.
func (c *Context) XML(status int, data interface{}) error {
	c.Response.Header().Set("Content-Type", "application/xml; charset=utf-8")
	c.Response.WriteHeader(status)
	return xml.NewEncoder(c.Response).Encode(data)
}

// ---------------------------------------------------------------------------
// File response methods
// ---------------------------------------------------------------------------

// validateFilePath cleans the path and rejects directory traversal attempts.
func validateFilePath(path string) (string, error) {
	cleaned := filepath.Clean(path)
	if strings.Contains(cleaned, "..") {
		return "", fmt.Errorf("invalid file path")
	}
	return cleaned, nil
}

// File serves a file from the given path.
func (c *Context) File(path string) error {
	path, err := validateFilePath(path)
	if err != nil {
		return err
	}
	http.ServeFile(c.Response, c.Request, path)
	return nil
}

// Download sends a file as an attachment with the given filename.
//
// The Content-Disposition header is emitted with both a legacy
// quoted-ASCII fallback ("filename=") and an RFC 5987 / 2231 encoded
// filename* parameter, so non-ASCII characters (e.g. "résumé.pdf")
// round-trip to modern clients while pre-RFC 5987 clients still
// receive a sensible ASCII name.
func (c *Context) Download(path string, filename string) error {
	var err error
	path, err = validateFilePath(path)
	if err != nil {
		return err
	}
	c.Response.Header().Set("Content-Disposition", buildContentDisposition(filename))
	http.ServeFile(c.Response, c.Request, path)
	return nil
}

// Attachment is an alias for Download.
func (c *Context) Attachment(path string, filename string) error {
	return c.Download(path, filename)
}

// buildContentDisposition constructs an attachment Content-Disposition
// header value that honours RFC 5987 (encoding of header parameters
// with non-ASCII data) and RFC 6266 (fallback syntax).
//
// The legacy "filename=" parameter is stripped to ASCII and CRLF-safe.
// The "filename*=" parameter carries the full original name in
// UTF-8 percent-encoded form so conformant clients (all modern
// browsers) render it verbatim.
func buildContentDisposition(filename string) string {
	base := filepath.Base(filename)
	base = strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(base)

	// Legacy fallback: ASCII-only, quotes escaped.
	fallback := asciiOnly(base)
	fallback = strings.ReplaceAll(fallback, `\`, `\\`)
	fallback = strings.ReplaceAll(fallback, `"`, `\"`)

	// RFC 5987 encoded form. Percent-encode everything outside a
	// conservative token set to stay on the right side of the spec.
	encoded := rfc5987Encode(base)

	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, fallback, encoded)
}

// asciiOnly replaces every non-ASCII rune (and ASCII control chars)
// with "_" so the result is a safe legacy filename= fallback.
func asciiOnly(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r > 0x7e {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// rfc5987Encode percent-encodes a UTF-8 string per RFC 5987 §3.2.1
// attr-char production. Only unreserved characters are left unescaped.
func rfc5987Encode(s string) string {
	// RFC 5987 attr-char = ALPHA / DIGIT / "!" / "#" / "$" / "&" / "+" /
	//   "-" / "." / "^" / "_" / "`" / "|" / "~"
	const safe = "!#$&+-.^_`|~"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9':
			b.WriteByte(c)
		case strings.IndexByte(safe, c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// SSE helper
// ---------------------------------------------------------------------------

// SSE sends a Server-Sent Event. On the first call it sets the appropriate
// SSE headers and clears the per-request write deadline so a long-lived
// stream isn't truncated by http.Server.WriteTimeout (Velocity defaults that
// to 30s as a slow-client guard, which is correct for normal endpoints but
// would tear an SSE stream every 30s, surfacing as ERR_HTTP2_PROTOCOL_ERROR
// on the browser when fronted by an HTTP/2 proxy).
//
// We deliberately do NOT emit a Connection: keep-alive header. It is the
// HTTP/1.1 default, hop-by-hop, and forbidden in HTTP/2 (RFC 7540 §8.1.2.2),
// so emitting it adds noise on h1 and breaks h2 deployments.
//
// Bounded session length should be implemented at the handler level (a
// ticker that returns a clean nil) rather than via a write deadline, which
// would cut bytes mid-frame and surface as a corrupt stream to the client.
func (c *Context) SSE(event string, data interface{}) error {
	if !c.sseStarted {
		PrepareStreamHeaders(c.Response)
		c.sseStarted = true
	}
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.Response, "event: %s\ndata: %s\n\n", event, jsonData)
	if err != nil {
		return err
	}
	if f, ok := c.Response.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// PrepareStream readies the response for a long-lived streaming body
// (Server-Sent Events, custom event streams, NDJSON, etc.). It sets the
// standard SSE headers (Content-Type: text/event-stream, Cache-Control:
// no-cache, X-Accel-Buffering: no) and clears the per-request write
// deadline so http.Server.WriteTimeout does not truncate the stream.
//
// Use this when the handler builds frames manually (fmt.Fprint /
// json.Encoder) instead of going through Context.SSE. After the call the
// handler is free to write directly to c.Response and Flush as needed.
//
// Idempotent: safe to call multiple times. Stream session bounding should
// be implemented via the request context or an in-handler ticker, NOT a
// write deadline (which cuts bytes mid-frame).
//
// SECURITY: clearing the write deadline removes the http.Server Slowloris
// guard for this connection. Handlers MUST defend against stuck-reader
// clients themselves:
//
//  1. Watch c.Request.Context().Done() and exit when the client TCP
//     connection drops.
//  2. Emit periodic heartbeat frames (e.g. "\n:keepalive\n\n" every 30s).
//     A blocked write on a dead-but-not-yet-RST socket will eventually
//     surface as an error once OS-level TCP keepalive declares the conn
//     dead, letting the handler clean up.
//  3. Apply per-route auth + body-size + rate limits at the router level
//     so an unauthenticated or hostile client cannot pin connections.
//
// Without these, a slow or hostile reader could hold an SSE stream open
// indefinitely, pinning a goroutine and leaking memory through the write
// buffer. PrepareStream itself does not enforce any of these; it only
// removes the deadline that would otherwise interfere with normal SSE.
func (c *Context) PrepareStream() {
	if !c.sseStarted {
		PrepareStreamHeaders(c.Response)
		c.sseStarted = true
	}
}

// PrepareStreamHeaders sets the standard streaming headers on w and clears
// the per-request write deadline. Exported so non-Context handlers (raw
// http.HandlerFunc shims, tests, etc.) can opt in to the same setup.
//
// The write-deadline clear uses http.NewResponseController so it propagates
// through Velocity's response wrappers (Unwrap is implemented in
// response_writer.go).
func PrepareStreamHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}
}

// ---------------------------------------------------------------------------
// Form and file helpers
// ---------------------------------------------------------------------------

// FormValue returns a form value by key.
func (c *Context) FormValue(key string) string {
	return c.Request.FormValue(key)
}

// FormFile returns the first file for the provided form key.
func (c *Context) FormFile(key string) (*multipart.FileHeader, error) {
	limit := DefaultMaxBodySize
	if l, ok := c.Get(bodyLimitKey).(int64); ok {
		limit = l
	}
	if err := c.Request.ParseMultipartForm(limit); err != nil {
		return nil, err
	}
	_, fh, err := c.Request.FormFile(key)
	return fh, err
}

// SaveFile saves an uploaded file to dst. The destination path must not
// contain ".." to prevent directory traversal.
func (c *Context) SaveFile(fh *multipart.FileHeader, dst string) error {
	var err error
	dst, err = validateFilePath(dst)
	if err != nil {
		return err
	}
	src, err := fh.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, src)
	return err
}

// ---------------------------------------------------------------------------
// Cookie delete helper
// ---------------------------------------------------------------------------

// DeleteCookie expires a cookie by name.
func (c *Context) DeleteCookie(name string) {
	c.SetCookie(&http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
}

// ---------------------------------------------------------------------------
// Flash data (validation errors + old input)
// ---------------------------------------------------------------------------

const (
	// FlashErrorsCookie is the cookie name for flash validation errors.
	FlashErrorsCookie = "_velocity_errors"
	// FlashInputCookie is the cookie name for flash old input.
	FlashInputCookie = "_velocity_old"
)

// WithErrors stashes validation errors as a flash cookie so they survive
// a redirect and are available on the next request.
func (c *Context) WithErrors(errors any) {
	writeFlashCookie(c.Response, FlashErrorsCookie, errors)
}

// WithInput stashes old form input as a flash cookie so it survives
// a redirect and is available on the next request.
func (c *Context) WithInput(input any) {
	writeFlashCookie(c.Response, FlashInputCookie, input)
}

// writeFlashCookie JSON-encodes value and sets it as a base64-encoded cookie.
func writeFlashCookie(w http.ResponseWriter, name string, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    base64.URLEncoding.EncodeToString(data),
		Path:     "/",
		MaxAge:   300, // 5 minutes; cleared on read
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// Validate checks the request against rules and automatically redirects back
// with flashed errors and old input if validation fails. Returns
// ErrValidationAborted when validation fails, the handler should return
// this error to the router, which will skip error handling since the
// redirect response has already been written.
//
//	func (h *Handler) Store(ctx *router.Context) error {
//	    if err := ctx.Validate(map[string][]string{
//	        "name":  {"required"},
//	        "email": {"required", "email", "unique:users,email"},
//	    }); err != nil {
//	        return err
//	    }
//	    // only reaches here if valid
//	}
func (c *Context) Validate(rules map[string][]string, messages ...map[string]string) error {
	if c.validateFn != nil {
		return c.validateFn(c, rules, messages...)
	}
	panic("velocity/router: validator not configured")
}

// ---------------------------------------------------------------------------
// Content negotiation
// ---------------------------------------------------------------------------

// acceptEntry is a single (media-type, q-value) pair parsed from an
// Accept header.
type acceptEntry struct {
	mime string
	q    float64
}

// Accepts parses the Accept header and returns the first offered type
// that the client accepts, ordered by q-value.
//
// If no Accept header is set, the first offered type is returned as a
// sensible default; if nothing matches, the empty string is returned.
func (c *Context) Accepts(offered ...string) string {
	accept := c.Request.Header.Get("Accept")
	if accept == "" {
		if len(offered) > 0 {
			return offered[0]
		}
		return ""
	}
	entries := parseAcceptHeader(accept)
	return selectOffered(entries, offered)
}

// parseAcceptHeader splits an Accept header into (mime, q) pairs
// sorted by descending q-value. Invalid or empty components are
// dropped silently, same as net/http's behaviour.
func parseAcceptHeader(header string) []acceptEntry {
	parts := strings.Split(header, ",")
	entries := make([]acceptEntry, 0, len(parts))
	for _, p := range parts {
		if e, ok := parseAcceptEntry(p); ok {
			entries = append(entries, e)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].q > entries[j].q
	})
	return entries
}

// parseAcceptEntry parses a single Accept header component such as
// "text/html;q=0.8". Returns false if the component is empty.
func parseAcceptEntry(raw string) (acceptEntry, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return acceptEntry{}, false
	}
	mediaType := raw
	q := 1.0
	if idx := strings.Index(raw, ";"); idx != -1 {
		mediaType = strings.TrimSpace(raw[:idx])
		q = parseQValue(raw[idx+1:])
	}
	return acceptEntry{mime: mediaType, q: q}, true
}

// parseQValue walks a list of ";"-separated parameters looking for
// q=<float>. Returns 1.0 on absence or parse failure.
func parseQValue(params string) float64 {
	for _, param := range strings.Split(params, ";") {
		param = strings.TrimSpace(param)
		if !strings.HasPrefix(param, "q=") {
			continue
		}
		if v, err := strconv.ParseFloat(strings.TrimPrefix(param, "q="), 64); err == nil {
			return v
		}
	}
	return 1.0
}

// selectOffered walks the ranked Accept entries and returns the first
// offered type that matches (exact or "*/*").
func selectOffered(entries []acceptEntry, offered []string) string {
	for _, e := range entries {
		for _, o := range offered {
			if e.mime == o || e.mime == "*/*" {
				return o
			}
		}
	}
	return ""
}
