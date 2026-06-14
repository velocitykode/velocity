package router

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
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
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/clientip"
	"github.com/velocitykode/velocity/resource"
	"github.com/velocitykode/velocity/scheduler"
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

// ServicesFromRequest returns the *app.Services stashed on r's context by
// WithServices, or nil when the request has not been routed through the
// Velocity pipeline. Packages outside router (e.g. bond) use this to reach
// shared services (crypto, etc.) without holding a *Context.
func ServicesFromRequest(r *http.Request) *app.Services {
	if r == nil {
		return nil
	}
	// A WithServices override layered above the matched route wins
	// (last-writer-wins); fall back to the bundled routeData otherwise.
	if s, ok := r.Context().Value(servicesCtxKey{}).(*app.Services); ok {
		return s
	}
	if rd, ok := r.Context().Value(routeDataKey{}).(*routeData); ok && rd.services != nil {
		return rd.services
	}
	return nil
}

// Param represents a single route parameter key-value pair.
type RouteParam struct {
	Key   string
	Value string
}

// Context wraps http.Request and http.ResponseWriter with helper methods.
//
// Router-owned wiring fields (services, trustedProxies,
// redirectAllowedHosts, fileRoot, validateFn, intendedFn) flow through
// ctxWiring; any new wiring field must be added to ctxWiring (and its
// applyWiring/snapshotWiring methods) AND to reset().
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
	// fileRoot is the kernel-enforced root under which File/Download/
	// SaveFile are permitted to operate. Carried from
	// Router.FileRootHandle() when the context is acquired from the
	// pool. A nil value means "no root opened, reject all file ops"
	// (which only happens when the configured directory could not be
	// opened, e.g. missing or permission denied). The handle is owned
	// by the router, the context never closes it.
	fileRoot   *os.Root
	validateFn func(c *Context, rules map[string][]string, messages ...map[string]string) error
	// intendedFn pulls the post-login "intended" URL from the session.
	// Wired during app init via Router.SetIntendedResolver so router need
	// not import auth. Returns "" when nothing is stashed.
	intendedFn func(c *Context) string
	// insecureFlashCookies opts flash cookies out of the Secure
	// attribute. Carried from app.Services.InsecureFlashCookies (a
	// validated dev/test-only opt-out). Stored inverted so the pool's
	// zero value after reset() means Secure.
	insecureFlashCookies bool
}

// NewContext creates a new Context from http.Request and http.ResponseWriter.
// If services were previously stashed on r.Context() (via the router pipeline),
// they are inherited automatically.
func NewContext(w http.ResponseWriter, r *http.Request) *Context {
	// Inherit services from either the bundled routeData (matched path)
	// or a standalone WithServices (not-found/static/external).
	svc := ServicesFromRequest(r)
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
		// Inherit the flash-cookie Secure decision with the services so
		// Wrap-built contexts agree with pool-built ones.
		insecureFlashCookies: svc != nil && svc.InsecureFlashCookies,
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
	if err != nil || math.IsInf(f, 0) {
		if len(defaultValue) > 0 {
			return defaultValue[0]
		}
		return 0
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

// AddHeader appends a response header value, preserving values already
// set by other middleware (use for list-valued headers like Vary).
// Names and values containing \r or \n are rejected to prevent header injection.
func (c *Context) AddHeader(name, value string) {
	if strings.ContainsAny(name, "\r\n") || strings.ContainsAny(value, "\r\n") {
		return
	}
	c.Response.Header().Add(name, value)
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
	c.Response.Header().Set("X-Content-Type-Options", "nosniff")
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

// IntendedSessionKey is the session-bag key under which auth middleware
// stashes the originally requested URL before bouncing an unauthenticated
// browser to a clean /login. Centralising the name here keeps the producer
// (auth's denyUnauthenticated) and the consumer (ctx.Intended, via the
// wired intendedFn resolver) in sync without an import cycle.
const IntendedSessionKey = "url.intended"

// Intended returns the safe redirect target for the "intended" post-login
// destination, falling back to fallback when no such target is set or
// when the stored value fails open-redirect validation.
//
// The destination is read (and consumed) from the session via the
// resolver wired by Router.SetIntendedResolver during app init: auth
// middleware stashed it under IntendedSessionKey when it bounced the
// unauthenticated request to /login. Reading is one-shot (pull): the
// resolver removes the key so a later navigation does not replay a stale
// destination. When no resolver is wired or nothing was stashed, fallback
// is used.
//
// The returned string is ALWAYS safe to pass straight to ctx.Redirect:
// it has been validated through the router's allowlist + scheme +
// slash-lookalike pipeline (see sanitizeRedirect). The canonical caller
// is ctx.RedirectToIntended.
//
// Contract:
//   - Safe relative paths ("/dashboard", "/admin/users") pass through.
//   - Absolute URLs to a host outside Router.RedirectAllowedHosts fall
//     back to fallback.
//   - Backslash and Unicode-slash lookalikes ("/\evil", "/／evil") fall
//     back to fallback.
//   - No stashed value (or no resolver wired) falls back to fallback.
//   - fallback itself is also sanitised so a buggy caller cannot
//     introduce its own open redirect via the fallback string.
func (c *Context) Intended(fallback string) string {
	fallback = sanitizeRedirect(fallback, c.redirectAllowedHosts)
	if c.intendedFn == nil {
		return fallback
	}
	raw := c.intendedFn(c)
	if raw == "" {
		return fallback
	}
	safe := sanitizeRedirect(raw, c.redirectAllowedHosts)
	// sanitizeRedirect returns "/" for any rejected input. Distinguish
	// "stored value rejected" from "stored value was literally /" by
	// re-comparing: if the raw input was anything other than "/" but
	// sanitised to "/", treat that as a rejection and prefer fallback.
	if safe == "/" && raw != "/" {
		return fallback
	}
	return safe
}

// RedirectToIntended issues a 303 See Other to the safe Intended()
// target (or fallback). This is the canonical caller for post-login
// flows: the auth middleware bounces the browser through
// /login?redirect=<original>, the login handler verifies the
// credentials, then calls ctx.RedirectToIntended("/") to ship the
// user back to where they were headed.
//
// The destination is open-redirect-safe by construction: ctx.Intended
// runs both the query value and the fallback through the same
// sanitiser ctx.Redirect uses. Handlers MUST NOT bypass this helper by
// reading the "redirect" query param directly, that string is
// untrusted user input and feeding it to ctx.Redirect via string
// concatenation would re-introduce the open redirect.
func (c *Context) RedirectToIntended(fallback string) error {
	return c.Redirect(http.StatusSeeOther, c.Intended(fallback))
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
// the RFC 7239 Forwarded header (preferred) or X-Forwarded-For /
// X-Real-IP (legacy) are consulted via internal/clientip.Extract so
// the framework speaks one IP-resolution policy across rate limit,
// throttle, audit log, exceptions, and this accessor.
func (c *Context) IP() string {
	if ip := clientip.ExtractString(c.Request, c.trustedProxies.IPNets()); ip != "" {
		return ip
	}
	return stripPortHost(c.Request.RemoteAddr)
}

// TrustedProxyNets returns the router-level trusted proxy networks
// installed for this request, in the form expected by
// internal/clientip.Extract. Middleware wanting to perform their own
// client-IP resolution (e.g. rate limiters that want to union an
// extra deployment-specific trust list with the router's) should
// call this so the framework speaks one trust policy.
//
// Returns nil when no proxies are trusted.
func (c *Context) TrustedProxyNets() []*net.IPNet {
	return c.trustedProxies.IPNets()
}

// ctxWiring is the canonical carrier for the router-owned per-request
// wiring on a Context. Every site that populates a Context from router
// state (matched routes, static dispatch, not-found, and the Timeout
// clone) goes through applyWiring so the field list lives in exactly
// one place. Any new wiring field must be added here, to applyWiring
// and snapshotWiring, AND to reset().
type ctxWiring struct {
	services             *app.Services
	trustedProxies       *TrustedProxies
	redirectAllowedHosts []string
	fileRoot             *os.Root
	validateFn           func(c *Context, rules map[string][]string, messages ...map[string]string) error
	intendedFn           func(c *Context) string
	insecureFlashCookies bool
}

// applyWiring installs the router-owned wiring fields on the context.
func (c *Context) applyWiring(w ctxWiring) {
	c.services = w.services
	c.trustedProxies = w.trustedProxies
	c.redirectAllowedHosts = w.redirectAllowedHosts
	c.fileRoot = w.fileRoot
	c.validateFn = w.validateFn
	c.intendedFn = w.intendedFn
	c.insecureFlashCookies = w.insecureFlashCookies
}

// snapshotWiring captures the context's wiring so it can be copied onto
// another Context (e.g. the Timeout clone) without listing fields at
// the call site.
func (c *Context) snapshotWiring() ctxWiring {
	return ctxWiring{
		services:             c.services,
		trustedProxies:       c.trustedProxies,
		redirectAllowedHosts: c.redirectAllowedHosts,
		fileRoot:             c.fileRoot,
		validateFn:           c.validateFn,
		intendedFn:           c.intendedFn,
		insecureFlashCookies: c.insecureFlashCookies,
	}
}

// reset clears the context for reuse by the sync.Pool. Every wiring
// field carried by ctxWiring must be zeroed here as well; a field
// missing from reset() leaks state across pooled requests.
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
	c.fileRoot = nil
	c.validateFn = nil
	c.intendedFn = nil
	c.insecureFlashCookies = false
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

// Wrap converts a HandlerFunc to http.HandlerFunc.
//
// Error mapping mirrors the router's default handleError path: a
// *HTTPError (direct or wrapped) responds with its code, echoing its
// message only for 4xx (client-facing by design); 5xx and non-HTTPError
// errors produce a generic body so server-side detail never reaches the
// client.
func Wrap(h HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := NewContext(w, r)
		if err := h(c); err != nil {
			var he *HTTPError
			if errors.As(err, &he) {
				if he.Code >= http.StatusInternalServerError {
					http.Error(w, http.StatusText(he.Code), he.Code)
				} else {
					http.Error(w, he.Message, he.Code)
				}
				return
			}
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
//   - "/\evil.com", "\\evil.com": browsers and intermediaries may
//     normalise "\" to "/", which would turn a leading "/\" into the
//     network-path reference "//". Mirrors bond.sanitizeRedirectURL's
//     backslash rejection (see bond/redirect.go).
//   - Unicode-similar slashes (U+FF0F FULLWIDTH SOLIDUS, U+29F8 BIG
//     SOLIDUS, U+2044 FRACTION SLASH, U+2215 DIVISION SLASH): some
//     normalisers fold these into ASCII "/", which again creates a
//     network-path reference. Reject conservatively before we trust the
//     leading character as a path separator.
func sanitizeRedirect(target string, allowedHosts []string) string {
	if target == "" {
		return "/"
	}
	// Reject backslash variants and Unicode-similar slash codepoints up
	// front so a downstream normaliser cannot turn "/\evil" or
	// "/／evil" into a protocol-relative reference.
	if containsSlashLookalike(target) {
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

// SanitizeRedirect validates a redirect URL against an explicit host
// allowlist, rewriting anything unsafe to "/". It is the canonical
// open-redirect sanitizer for the framework; packages that perform
// their own redirects (e.g. bond) delegate to it rather than keeping a
// parallel copy. See sanitizeRedirect for the exact rules.
func SanitizeRedirect(target string, allowedHosts []string) string {
	return sanitizeRedirect(target, allowedHosts)
}

// containsSlashLookalike reports whether target contains a backslash or a
// Unicode codepoint that some clients or intermediaries normalise to "/".
// These are rejected before the path-vs-host decision because a leading
// "/" followed by any of them (e.g. "/\evil", "/／evil") can become the
// network-path reference "//evil" after normalisation, which is an open
// redirect.
//
// Codepoints covered:
//   - U+005C  REVERSE SOLIDUS (ASCII backslash)
//   - U+FF0F  FULLWIDTH SOLIDUS
//   - U+29F8  BIG SOLIDUS
//   - U+2044  FRACTION SLASH
//   - U+2215  DIVISION SLASH
func containsSlashLookalike(target string) bool {
	for _, r := range target {
		switch r {
		case '\\', '／', '⧸', '⁄', '∕':
			return true
		}
	}
	return false
}

// Forbidden sends a 403 error response
func (c *Context) Forbidden(message ...string) error {
	return c.httpError(http.StatusForbidden, "Forbidden", message)
}

// SetServices sets the service container on this context and stashes it
// on r.Context() so that any downstream Wrap / NewContext inherits it.
func (c *Context) SetServices(s *app.Services) {
	c.services = s
	c.insecureFlashCookies = s != nil && s.InsecureFlashCookies
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

// DB returns the ORM database interface as the stdlib-only contract.Database.
// The stored value is always the concrete *orm.Manager, which satisfies this
// contract with no adapter.
//
// contract.Database intentionally omits the driver-facing methods
// (DefaultDriver, Connection, AddConnection) so that ./router carries no heavy
// driver packages. This is a deliberate narrowing of the surface, not an
// accidental capability loss. Handlers that need those methods recover the
// wider interface with the supported escape hatch:
//
//	db, ok := c.DB().(orm.Database) // orm.Database includes the driver methods
//
// (orm cannot expose a FromContext helper here: orm importing router would
// create an import cycle, so the type assertion is the canonical recovery path.)
func (c *Context) DB() contract.Database {
	s := c.mustServices()
	requireService(c, s.DB, "database")
	return s.DB
}

// Cache returns the cache manager interface.
func (c *Context) Cache() contract.CacheManager {
	s := c.mustServices()
	requireService(c, s.Cache, "cache")
	return s.Cache
}

// Log returns the logger.
func (c *Context) Log() contract.Logger {
	s := c.mustServices()
	requireService(c, s.Log, "log")
	return s.Log
}

// Queue returns the queue driver.
func (c *Context) Queue() contract.QueueDriver {
	s := c.mustServices()
	requireService(c, s.Queue, "queue")
	return s.Queue
}

// Storage returns the storage manager interface.
func (c *Context) Storage() contract.StorageManager {
	s := c.mustServices()
	requireService(c, s.Storage, "storage")
	return s.Storage
}

// Mail returns the mailer.
func (c *Context) Mail() contract.Mailer {
	s := c.mustServices()
	requireService(c, s.Mail, "mail")
	return s.Mail
}

// Notification returns the notification interface.
func (c *Context) Notification() contract.Notifier {
	s := c.mustServices()
	requireService(c, s.Notification, "notification")
	return s.Notification
}

// Events returns the event dispatcher.
func (c *Context) Events() contract.Dispatcher {
	s := c.mustServices()
	requireService(c, s.Events, "events")
	return s.Events
}

// Crypto returns the encryptor.
func (c *Context) Crypto() contract.Encryptor {
	s := c.mustServices()
	requireService(c, s.Crypto, "crypto")
	return s.Crypto
}

// Validator returns the validator.
func (c *Context) Validator() contract.Validator {
	s := c.mustServices()
	requireService(c, s.Validator, "validator")
	return s.Validator
}

// Exceptions returns the exception handler interface.
func (c *Context) Exceptions() contract.ExceptionHandler {
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
	// Wrap only when the BodyLimit middleware has not already installed
	// its own MaxBytesReader (bodyLimitKey set). Wrapping again would
	// stack a second reader on top of the middleware's and re-apply a
	// limit over the operator-configured one.
	if c.Get(bodyLimitKey) == nil {
		c.Request.Body = http.MaxBytesReader(c.Response, c.Request.Body, DefaultMaxBodySize)
	}
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
	ValidationRules() contract.ValidationRules
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

// validateFilePath performs structural validation on a relative user
// path before it is handed to an *os.Root for kernel-enforced
// containment. It does NOT touch the filesystem; symlink and traversal
// containment are enforced by os.Root at open time, which closes the
// TOCTOU window an Lstat-then-Open implementation would have.
//
// Only relative paths are accepted. The function rejects:
//   - absolute paths (e.g. "/etc/passwd")
//   - paths containing a ".." segment after cleaning (directory traversal)
//   - paths containing NUL bytes (null-byte injection)
//
// The returned path is the cleaned relative form, safe to pass to
// (*os.Root).Open or (*os.Root).OpenFile.
func validateFilePath(path string) (string, error) {
	if strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("invalid file path")
	}
	cleaned := filepath.Clean(path)
	if strings.ContainsRune(cleaned, 0) {
		return "", fmt.Errorf("invalid file path")
	}
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, string(filepath.Separator)) {
		return "", fmt.Errorf("invalid file path")
	}
	// Reject ".." as a path segment (not as a substring of a filename).
	if cleaned == ".." {
		return "", fmt.Errorf("invalid file path")
	}
	for _, sep := range []string{string(filepath.Separator), "/"} {
		if strings.HasPrefix(cleaned, ".."+sep) ||
			strings.Contains(cleaned, sep+".."+sep) ||
			strings.HasSuffix(cleaned, sep+"..") {
			return "", fmt.Errorf("invalid file path")
		}
	}
	return cleaned, nil
}

// fileRootOrError returns the context's *os.Root, or an error if no
// root is wired. File/Download/SaveFile all funnel through this so the
// nil-root case (e.g. router never opened a root, or test context
// without a root) surfaces as a clear error rather than a panic.
func (c *Context) fileRootOrError() (*os.Root, error) {
	if c.fileRoot == nil {
		return nil, fmt.Errorf("velocity/router: no file root configured")
	}
	return c.fileRoot, nil
}

// defaultPrivateNoStore sets `Cache-Control: private, no-store` on the
// response if (and only if) the caller has not already set the header.
// File / Download serve auth-gated bytes; without an explicit cache
// directive, shared intermediaries (corporate proxies, CDNs) may cache
// the body keyed on URL alone, leaking it to subsequent unauthenticated
// requesters. Caller-set values are preserved so a handler that wants
// public caching can override with c.SetHeader before invoking.
func (c *Context) defaultPrivateNoStore() {
	if c.Response == nil {
		return
	}
	h := c.Response.Header()
	if h.Get("Cache-Control") != "" {
		return
	}
	h.Set("Cache-Control", "private, no-store")
}

// File serves a file from the given path, resolved relative to the
// router's FileRoot. Containment is kernel-enforced via *os.Root, so
// a symlink swap between path validation and the actual open cannot
// escape the root.
//
// Sets `Cache-Control: private, no-store` by default; a caller-set
// Cache-Control header is preserved.
func (c *Context) File(path string) error {
	root, err := c.fileRootOrError()
	if err != nil {
		return err
	}
	rel, err := validateFilePath(path)
	if err != nil {
		return err
	}
	f, err := OpenFileIn(root, rel)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("velocity/router: path is a directory")
	}
	c.defaultPrivateNoStore()
	http.ServeContent(c.Response, c.Request, filepath.Base(rel), info.ModTime(), f)
	return nil
}

// Download sends a file as an attachment with the given filename.
//
// path is resolved relative to the router's FileRoot. Containment is
// kernel-enforced via *os.Root. The Content-Disposition header is
// emitted with both a legacy quoted-ASCII fallback ("filename=") and
// an RFC 5987 / 2231 encoded filename* parameter, so non-ASCII
// characters (e.g. "résumé.pdf") round-trip to modern clients while
// pre-RFC 5987 clients still receive a sensible ASCII name.
//
// Sets `Cache-Control: private, no-store` by default; a caller-set
// Cache-Control header is preserved.
func (c *Context) Download(path string, filename string) error {
	root, err := c.fileRootOrError()
	if err != nil {
		return err
	}
	rel, err := validateFilePath(path)
	if err != nil {
		return err
	}
	f, err := OpenFileIn(root, rel)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("velocity/router: path is a directory")
	}
	c.defaultPrivateNoStore()
	c.Response.Header().Set("Content-Disposition", buildContentDisposition(filename))
	http.ServeContent(c.Response, c.Request, filepath.Base(rel), info.ModTime(), f)
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
//
// Event names containing \r or \n are rejected with an error before anything
// is written, to prevent injection of extra SSE fields. Data is JSON-encoded
// and therefore newline-safe.
func (c *Context) SSE(event string, data interface{}) error {
	if strings.ContainsAny(event, "\r\n") {
		return fmt.Errorf("router: SSE event name %q contains \\r or \\n; rejected to prevent SSE field injection", event)
	}
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
//
// Unless the BodyLimit middleware has already wrapped the request body,
// FormValue caps the payload at DefaultMaxBodySize via http.MaxBytesReader
// before delegating to (*Request).FormValue. Without the cap a
// multipart/form-data request would be parsed with no overall body bound,
// spilling unbounded data to os.TempDir(). When the limit is exceeded the
// underlying parse fails and the empty string is returned, matching the
// stdlib behavior for unparseable bodies.
func (c *Context) FormValue(key string) string {
	if c.Get(bodyLimitKey) == nil {
		c.Request.Body = http.MaxBytesReader(c.Response, c.Request.Body, DefaultMaxBodySize)
	}
	return c.Request.FormValue(key)
}

// formFileMaxMemory is the in-memory threshold passed to
// (*Request).ParseMultipartForm. Past this threshold the multipart
// reader spills parts to os.TempDir(). Keeping this small bounds the
// per-request RAM cost; the full upload size is bounded separately by
// http.MaxBytesReader on the request body (see FormFile below).
const formFileMaxMemory int64 = 1 << 20 // 1 MiB

// uploadedFileMode is the secret-tier permission used for files saved by
// (*Context).SaveUploadedFile. Uploaded payloads may carry PII or
// partial secrets, so other local users must not be able to read them.
// Mirrors storage.LocalDriver's invariant.
const uploadedFileMode os.FileMode = 0o600

// FormFile returns the first file for the provided form key.
//
// Unless the BodyLimit middleware has already wrapped the request body,
// FormFile caps the multipart payload at DefaultMaxBodySize via
// http.MaxBytesReader before parsing. The on-disk spill threshold
// passed to ParseMultipartForm is the small fixed constant
// formFileMaxMemory; the body-size cap is enforced by MaxBytesReader,
// not by ParseMultipartForm's maxMemory argument, which controls only
// how much of the parsed form lives in RAM before spilling to
// os.TempDir().
//
// Routes that legitimately need to accept large uploads must install
// router.BodyLimit(N) for their chain so the cap matches the use case.
func (c *Context) FormFile(key string) (*multipart.FileHeader, error) {
	if c.Get(bodyLimitKey) == nil {
		c.Request.Body = http.MaxBytesReader(c.Response, c.Request.Body, DefaultMaxBodySize)
	}
	if err := c.Request.ParseMultipartForm(formFileMaxMemory); err != nil {
		return nil, err
	}
	_, fh, err := c.Request.FormFile(key)
	return fh, err
}

// ErrFileSizeExceeded is returned by SaveFile when the upload stream
// produces more bytes than the declared multipart.FileHeader.Size (or
// the caller-supplied MaxFileSize cap). A lying Content-Length is the
// usual cause; the partially-written destination file is removed
// before the error is surfaced.
var ErrFileSizeExceeded = errors.New("velocity/router: uploaded file exceeds declared size")

// SaveFile saves an uploaded file to dst. dst is resolved relative to
// the router's FileRoot and must resolve to a location contained
// within that root. Containment is kernel-enforced via *os.Root, so
// a symlinked parent pointing outside the root is rejected at
// OpenFile time with no TOCTOU window between validation and create.
//
// Optional FileValidationOption values (MaxFileSize, AllowedExtensions,
// AllowedMIMETypes) are evaluated via ValidateFile before any bytes are
// written. Validation failure returns the validator error and writes
// nothing.
//
// Defence in depth: the copy is bounded by io.LimitReader at
// fh.Size+1 (or the MaxFileSize option when smaller). If the source
// stream produces more than the declared size, the partial file is
// removed and ErrFileSizeExceeded is returned. This guards against
// hostile clients whose Content-Length lies.
func (c *Context) SaveFile(fh *multipart.FileHeader, dst string, opts ...FileValidationOption) error {
	root, err := c.fileRootOrError()
	if err != nil {
		return err
	}
	rel, err := validateFilePath(dst)
	if err != nil {
		return err
	}
	if err := c.ValidateFile(fh, opts...); err != nil {
		return err
	}

	sizeCap := fh.Size
	var cfg fileValidationConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.maxSize > 0 && cfg.maxSize < sizeCap {
		sizeCap = cfg.maxSize
	}

	src, err := fh.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	// Default to owner-only (0o600) so uploaded request bodies do not
	// land on disk world-readable. Mirrors storage.LocalDriver.Put.
	// Note: OpenFile filters mode through umask AND preserves the
	// mode of a pre-existing target, so we follow up with Chmod to
	// pin the invariant regardless of starting state.
	out, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, uploadedFileMode)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		return fmt.Errorf("velocity/router: path %q escapes root: %w", rel, errors.Join(ErrPathOutsideRoot, err))
	}
	defer out.Close()
	if chmodErr := out.Chmod(uploadedFileMode); chmodErr != nil {
		_ = root.Remove(rel)
		return fmt.Errorf("velocity/router: chmod uploaded file: %w", chmodErr)
	}

	written, err := io.Copy(out, io.LimitReader(src, sizeCap+1))
	if err != nil {
		_ = root.Remove(rel)
		return err
	}
	if written > sizeCap {
		_ = root.Remove(rel)
		return ErrFileSizeExceeded
	}
	return nil
}

// ---------------------------------------------------------------------------
// Cookie delete helper
// ---------------------------------------------------------------------------

// DeleteCookie expires a cookie by name. The deletion cookie carries the
// same Secure/SameSite attributes as the framework's other cookie
// deletions: Secure follows the validated app cookie config (a Secure
// deletion sent over plain HTTP is dropped by browsers, so the dev/test
// opt-out must apply here too), SameSite is Lax.
func (c *Context) DeleteCookie(name string) {
	c.SetCookie(FlashCookie(name, "", -1, !c.insecureFlashCookies))
}

// ---------------------------------------------------------------------------
// Flash data (validation errors + old input)
// ---------------------------------------------------------------------------

const (
	// FlashErrorsCookie is the cookie name for flash validation errors.
	FlashErrorsCookie = "_velocity_errors"
	// FlashInputCookie is the cookie name for flash old input.
	FlashInputCookie = "_velocity_old"

	// MaxFlashCookieSize bounds the size of a flash cookie value (the
	// authenticated, base64-encoded ciphertext). 4 KiB is the per-cookie
	// limit common to all major browsers; oversized cookies are rejected
	// on read to prevent an attacker from forcing a large decrypt path.
	MaxFlashCookieSize = 4096

	// flashErrorsAAD / flashInputAAD domain-separate the two flash
	// cookies so a ciphertext valid for "_velocity_errors" cannot be
	// replayed as "_velocity_old" (or vice versa) under the same app
	// key. The "v1" tag reserves room for future format migrations.
	flashErrorsAAD = "velocity:flash-cookie:errors:v1"
	flashInputAAD  = "velocity:flash-cookie:old:v1"
)

// flashAADFor returns the AAD label bound into the ciphertext for the
// given flash cookie name. Returns an empty string for unknown names so
// the caller fails closed without panicking on a typo.
func flashAADFor(name string) string {
	switch name {
	case FlashErrorsCookie:
		return flashErrorsAAD
	case FlashInputCookie:
		return flashInputAAD
	}
	return ""
}

// WithErrors stashes validation errors as a flash cookie so they survive
// a redirect and are available on the next request. The cookie payload is
// encrypted with the app key via AES-GCM (or AES-CBC+HMAC, whichever the
// app's crypto.Encryptor was configured with), with the cookie name's AAD
// label bound into the authentication check in both modes, so a
// sibling-domain cookie injection cannot forge errors that bond would
// inject into props on the next render, and an errors ciphertext can
// never be replayed as old input (or vice versa).
//
// Silently no-ops when the app has no crypto.Encryptor wired (e.g. raw
// test contexts) so callers do not have to handle a failure mode that
// only manifests in misconfigured environments. Operators should treat
// a missing encryptor as a configuration bug; the lack of a flash
// cookie on the response is the visible symptom.
func (c *Context) WithErrors(errors any) {
	writeFlashCookie(c.Response, c.flashEncryptor(), FlashErrorsCookie, errors, !c.insecureFlashCookies)
}

// WithInput stashes old form input as a flash cookie so it survives
// a redirect and is available on the next request. See WithErrors for
// authentication and configuration notes.
func (c *Context) WithInput(input any) {
	writeFlashCookie(c.Response, c.flashEncryptor(), FlashInputCookie, input, !c.insecureFlashCookies)
}

// flashEncryptor returns the app's crypto.Encryptor when services are
// wired, else nil. Callers must handle nil by skipping the cookie write
// rather than emitting an unauthenticated payload.
func (c *Context) flashEncryptor() contract.Encryptor {
	if c.services == nil {
		return nil
	}
	return c.services.Crypto
}

// SealFlash JSON-encodes value, encrypts it with enc under the AAD bound
// to name, and returns the cookie value. Returns the empty string and an
// error when enc is nil, name is unrecognized, or encryption fails. The
// returned string is safe to set as an HTTP cookie value (URL-base64,
// no separators).
//
// Exposed so packages that read or write flash cookies outside the
// router pipeline (e.g. bond/flash.go on the read path) can produce
// payloads that this package will accept on the next request.
func SealFlash(enc contract.Encryptor, name string, value any) (string, error) {
	if enc == nil {
		return "", errors.New("velocity/router: flash encryptor not configured")
	}
	aad := flashAADFor(name)
	if aad == "" {
		return "", fmt.Errorf("velocity/router: unknown flash cookie name %q", name)
	}
	plaintext, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	// Both AES modes bind the AAD: GCM via the AEAD tag, CBC via the
	// HMAC framing. No non-AAD fallback exists; a ciphertext sealed
	// under one cookie name can never verify under the other.
	return enc.EncryptBytesWithAAD(plaintext, []byte(aad))
}

// OpenFlash inverts SealFlash. Returns the decoded JSON value on
// success. Returns (nil, error) when the cookie is missing the AAD
// binding for name, is over MaxFlashCookieSize, fails decryption, or
// does not contain valid JSON. Callers MUST treat any error as "no
// flash data" and never surface the error to the client.
func OpenFlash(enc contract.Encryptor, name, cookieValue string) (any, error) {
	if enc == nil {
		return nil, errors.New("velocity/router: flash encryptor not configured")
	}
	if cookieValue == "" {
		return nil, errors.New("velocity/router: empty flash cookie")
	}
	if len(cookieValue) > MaxFlashCookieSize {
		return nil, fmt.Errorf("velocity/router: flash cookie exceeds %d bytes", MaxFlashCookieSize)
	}
	aad := flashAADFor(name)
	if aad == "" {
		return nil, fmt.Errorf("velocity/router: unknown flash cookie name %q", name)
	}
	// No non-AAD fallback: a cookie that does not verify under this
	// name's AAD (wrong name, tampered, or sealed via plain
	// EncryptBytes) is rejected outright. Flash cookies sealed by the
	// pre-AAD CBC fallback stop decoding after an upgrade, which only
	// drops in-flight flash data (5-minute cookies) once per deploy.
	plaintext, err := enc.DecryptBytesWithAAD(cookieValue, []byte(aad))
	if err != nil {
		return nil, err
	}
	var result any
	if err := json.Unmarshal(plaintext, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// FlashCookie builds the canonical framework cookie: Path=/, HttpOnly,
// SameSite=Lax, with the given Secure attribute. Every site that writes
// or clears a flash cookie (writeFlashCookie here, bond's clear path)
// and DeleteCookie MUST build it through this helper so the attributes
// - Secure in particular - never diverge between write and clear: a
// Secure write paired with a non-Secure clear (or vice versa over plain
// HTTP, where browsers drop Secure cookies) leaves the cookie
// unclearable.
//
// secure should be true unless the app's validated session-cookie
// config opted out (app.Services.InsecureFlashCookies, dev/test only).
// Pass a negative maxAge to delete the cookie.
func FlashCookie(name, value string, maxAge int, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// writeFlashCookie encrypts value with enc and sets it as an HttpOnly,
// SameSite=Lax cookie with the given Secure attribute. Silently no-ops
// when enc is nil or encryption fails so the handler never blocks on a
// flash-write failure (the missing cookie surfaces on the next render
// as the absence of flashed errors / old input).
func writeFlashCookie(w http.ResponseWriter, enc contract.Encryptor, name string, value any, secure bool) {
	sealed, err := SealFlash(enc, name, value)
	if err != nil {
		return
	}
	// 5 minutes; cleared on read.
	http.SetCookie(w, FlashCookie(name, sealed, 300, secure))
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
