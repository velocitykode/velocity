package router

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/exceptions"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
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
	values         map[string]interface{}
	services       *app.Services
	sseStarted     bool
	trustedProxies []string
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
// If trusted proxies are configured and the remote address is in the trusted
// list, X-Forwarded-For is consulted and the first non-trusted IP is returned.
func (c *Context) IP() string {
	addr := c.Request.RemoteAddr
	// Strip port
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	if len(c.trustedProxies) > 0 && c.isTrustedProxy(host) {
		if xff := c.Request.Header.Get("X-Forwarded-For"); xff != "" {
			ips := strings.Split(xff, ",")
			// Walk from right to left, return first non-trusted IP
			for i := len(ips) - 1; i >= 0; i-- {
				ip := strings.TrimSpace(ips[i])
				if ip != "" && !c.isTrustedProxy(ip) {
					return ip
				}
			}
		}
	}

	return host
}

// isTrustedProxy checks if the given IP is in the trusted proxies list.
func (c *Context) isTrustedProxy(ip string) bool {
	for _, trusted := range c.trustedProxies {
		if trusted == ip {
			return true
		}
	}
	return false
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

// authGateChecker is a local interface satisfied by *auth.Manager to avoid
// importing pkg/auth (which imports pkg/router).
type authGateChecker interface {
	GateAllows(r *http.Request, ability string, args ...interface{}) bool
	GateAuthorize(r *http.Request, ability string, args ...interface{}) error
}

// Can returns true if the authenticated user is allowed to perform the given
// ability. Returns false when auth is not configured or no user is logged in.
func (c *Context) Can(ability string, args ...interface{}) bool {
	if c.services == nil || c.services.Auth == nil {
		return false
	}
	checker, ok := c.services.Auth.(authGateChecker)
	if !ok {
		return false
	}
	return checker.GateAllows(c.Request, ability, args...)
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
	checker, ok := c.services.Auth.(authGateChecker)
	if !ok {
		return NewHTTPError(http.StatusForbidden)
	}
	if err := checker.GateAuthorize(c.Request, ability, args...); err != nil {
		return NewHTTPError(http.StatusForbidden)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Multi-format binding
// ---------------------------------------------------------------------------

// BindForm parses form data and maps it to v using `form` struct tags.
func (c *Context) BindForm(v interface{}) error {
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
	c.Request.Body = http.MaxBytesReader(c.Response, c.Request.Body, DefaultMaxBodySize)
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
func (c *Context) BindValid(v interface{}) error {
	if err := c.Bind(v); err != nil {
		return err
	}
	if c.services == nil || c.services.Validator == nil {
		return nil
	}
	if val, ok := v.(Validatable); ok {
		dataMap := structToMap(v)
		_, err := c.services.Validator.Validate(dataMap, val.ValidationRules())
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

// File serves a file from the given path.
func (c *Context) File(path string) error {
	if strings.Contains(path, "..") {
		return fmt.Errorf("invalid file path")
	}
	path = filepath.Clean(path)
	http.ServeFile(c.Response, c.Request, path)
	return nil
}

// Download sends a file as an attachment with the given filename.
func (c *Context) Download(path string, filename string) error {
	if strings.Contains(path, "..") {
		return fmt.Errorf("invalid file path")
	}
	path = filepath.Clean(path)
	c.Response.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	http.ServeFile(c.Response, c.Request, path)
	return nil
}

// Attachment is an alias for Download.
func (c *Context) Attachment(path string, filename string) error {
	return c.Download(path, filename)
}

// ---------------------------------------------------------------------------
// SSE helper
// ---------------------------------------------------------------------------

// SSE sends a Server-Sent Event. On the first call it sets the appropriate
// headers (Content-Type, Cache-Control, Connection) and flushes.
func (c *Context) SSE(event string, data interface{}) error {
	if !c.sseStarted {
		c.Response.Header().Set("Content-Type", "text/event-stream")
		c.Response.Header().Set("Cache-Control", "no-cache")
		c.Response.Header().Set("Connection", "keep-alive")
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

// ---------------------------------------------------------------------------
// Form and file helpers
// ---------------------------------------------------------------------------

// FormValue returns a form value by key.
func (c *Context) FormValue(key string) string {
	return c.Request.FormValue(key)
}

// FormFile returns the first file for the provided form key.
func (c *Context) FormFile(key string) (*multipart.FileHeader, error) {
	if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
		return nil, err
	}
	_, fh, err := c.Request.FormFile(key)
	return fh, err
}

// SaveFile saves an uploaded file to dst. The destination path must not
// contain ".." to prevent directory traversal.
func (c *Context) SaveFile(fh *multipart.FileHeader, dst string) error {
	if strings.Contains(dst, "..") {
		return fmt.Errorf("invalid destination path")
	}
	dst = filepath.Clean(dst)
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
// Content negotiation
// ---------------------------------------------------------------------------

// Accepts parses the Accept header and returns the first offered type that
// the client accepts (ordered by quality value). Returns "" if no match.
func (c *Context) Accepts(offered ...string) string {
	accept := c.Request.Header.Get("Accept")
	if accept == "" {
		if len(offered) > 0 {
			return offered[0]
		}
		return ""
	}

	type entry struct {
		mime string
		q    float64
	}

	parts := strings.Split(accept, ",")
	entries := make([]entry, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		mime := p
		q := 1.0
		if idx := strings.Index(p, ";"); idx != -1 {
			mime = strings.TrimSpace(p[:idx])
			params := p[idx+1:]
			for _, param := range strings.Split(params, ";") {
				param = strings.TrimSpace(param)
				if strings.HasPrefix(param, "q=") {
					if v, err := strconv.ParseFloat(strings.TrimPrefix(param, "q="), 64); err == nil {
						q = v
					}
				}
			}
		}
		entries = append(entries, entry{mime: mime, q: q})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].q > entries[j].q
	})

	for _, e := range entries {
		for _, o := range offered {
			if e.mime == o || e.mime == "*/*" {
				return o
			}
		}
	}
	return ""
}
