package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/velocitykode/velocity/router"
)

// auditSalt is a process-scoped random salt used to hash remote addresses
// before they are logged. Regenerated on each process start so logs from
// different runs cannot be correlated by IP.
var (
	auditSaltOnce sync.Once
	auditSalt     []byte
)

// SetAuditSalt allows tests (or an operator with a fixed-salt requirement)
// to install a deterministic salt for the PII hasher. In production, leave
// this unset so a random salt is generated lazily.
func SetAuditSalt(salt []byte) {
	auditSaltOnce.Do(func() {}) // mark as initialized
	auditSalt = append(auditSalt[:0], salt...)
}

func getAuditSalt() []byte {
	auditSaltOnce.Do(func() {
		if auditSalt != nil {
			return
		}
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			// Degrade to an empty salt rather than panicking; the hash
			// still provides some obfuscation and we refuse to crash
			// because of log-line instrumentation.
			buf = []byte{}
		}
		auditSalt = buf
	})
	return auditSalt
}

// hashRemoteAddr produces a short hex digest of the client IP using a
// per-process salt. Returns an empty string for blank input. The digest is
// stable within a process lifetime so correlated requests are still
// recognisable in logs, but it is not reversible.
func hashRemoteAddr(remote string) string {
	if remote == "" {
		return ""
	}
	// Strip the port if present so log lines aren't noisy.
	host, _, err := net.SplitHostPort(remote)
	if err != nil || host == "" {
		host = remote
	}
	h := hmac.New(sha256.New, getAuditSalt())
	h.Write([]byte(host))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8])
}

// wantsJSON returns true if the request expects a JSON response.
func wantsJSON(r *http.Request) bool {
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		return true
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/json")
}

// denyUnauthenticated returns a 401 JSON response for API requests or redirects
// to /login for HTML requests. Shared by all auth-requiring middleware.
// The manager's logger records the denial when installed.
func denyUnauthenticated(manager *Manager, c *router.Context) error {
	manager.logWarn("velocity/auth: authentication required", "method", c.Request.Method, "path", c.Request.URL.Path, "ip_hash", hashRemoteAddr(c.Request.RemoteAddr))
	if wantsJSON(c.Request) {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Unauthenticated."})
	}
	redirectURL := c.Request.URL.Path
	if c.Request.URL.RawQuery != "" {
		redirectURL += "?" + c.Request.URL.RawQuery
	}
	escapedURL := url.QueryEscape(redirectURL)
	return c.Redirect(http.StatusSeeOther, "/login?redirect="+escapedURL)
}

// denyForbidden returns a 403 JSON response for API requests or a plain 403
// status for HTML requests. The manager's logger records the denial when
// installed.
func denyForbidden(manager *Manager, c *router.Context) error {
	manager.logWarn("velocity/auth: authorization denied", "method", c.Request.Method, "path", c.Request.URL.Path, "ip_hash", hashRemoteAddr(c.Request.RemoteAddr))
	if wantsJSON(c.Request) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "Forbidden."})
	}
	c.Response.WriteHeader(http.StatusForbidden)
	return nil
}

// FromContext extracts the *Manager from a router.Context.
// Returns nil if auth is not configured.
func FromContext(ctx *router.Context) *Manager {
	m, _ := ctx.Auth().(*Manager)
	return m
}

// AuthMiddleware returns a router.MiddlewareFunc that requires authentication
// using the provided Manager instance.
func AuthMiddleware(manager *Manager) router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			if !manager.Check(c.Request) {
				return denyUnauthenticated(manager, c)
			}
			return next(c)
		}
	}
}

// RequireRole returns middleware that checks the authenticated user has the
// given role. Returns 401 if not authenticated, 403 if role check fails.
func RequireRole(manager *Manager, role string) router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			if !manager.Check(c.Request) {
				return denyUnauthenticated(manager, c)
			}
			user := manager.User(c.Request)
			if !manager.Gate().HasRole(user, role) {
				return denyForbidden(manager, c)
			}
			return next(c)
		}
	}
}

// RequireAnyRole returns middleware that checks the authenticated user has at
// least one of the given roles. Returns 401 if not authenticated, 403 if no
// matching role.
func RequireAnyRole(manager *Manager, roles ...string) router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			if !manager.Check(c.Request) {
				return denyUnauthenticated(manager, c)
			}
			user := manager.User(c.Request)
			if !manager.Gate().HasAnyRole(user, roles...) {
				return denyForbidden(manager, c)
			}
			return next(c)
		}
	}
}

// RequireAllRoles returns middleware that checks the authenticated user has ALL
// of the given roles. Returns 401 if not authenticated, 403 if any role is
// missing.
func RequireAllRoles(manager *Manager, roles ...string) router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			if !manager.Check(c.Request) {
				return denyUnauthenticated(manager, c)
			}
			user := manager.User(c.Request)
			if !manager.Gate().HasAllRoles(user, roles...) {
				return denyForbidden(manager, c)
			}
			return next(c)
		}
	}
}

// AuthorizeMiddleware returns middleware that checks if the authenticated user
// can perform the given ability. An optional resourceFunc can provide the
// resource argument for the gate check. Returns 401 if not authenticated, 403
// if the ability is denied.
func AuthorizeMiddleware(manager *Manager, ability string, resourceFunc ...func(*router.Context) interface{}) router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			if !manager.Check(c.Request) {
				return denyUnauthenticated(manager, c)
			}
			user := manager.User(c.Request)
			var allowed bool
			if len(resourceFunc) > 0 && resourceFunc[0] != nil {
				resource := resourceFunc[0](c)
				allowed = manager.Gate().Allows(user, ability, resource)
			} else {
				allowed = manager.Gate().Allows(user, ability)
			}
			if !allowed {
				return denyForbidden(manager, c)
			}
			return next(c)
		}
	}
}

// GuestMiddleware returns middleware that only allows unauthenticated users.
// Authenticated users receive a 403 JSON response for API requests or are
// redirected to "/" for HTML requests.
func GuestMiddleware(manager *Manager) router.MiddlewareFunc {
	return GuestMiddlewareWithRedirect(manager, "/")
}

// GuestMiddlewareWithRedirect returns middleware that only allows
// unauthenticated users. Authenticated users receive a 403 JSON response for
// API requests or are redirected to the given URL for HTML requests.
func GuestMiddlewareWithRedirect(manager *Manager, redirectTo string) router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			if manager.Check(c.Request) {
				if wantsJSON(c.Request) {
					return c.JSON(http.StatusForbidden, map[string]string{"error": "Already authenticated."})
				}
				return c.Redirect(http.StatusSeeOther, redirectTo)
			}
			return next(c)
		}
	}
}
