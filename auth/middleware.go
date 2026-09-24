package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/clientip"
	"github.com/velocitykode/velocity/router"
)

// auditSalt is a process-scoped random salt used to hash remote addresses
// before they are logged. Regenerated on each process start so logs from
// different runs cannot be correlated by IP.
var (
	auditSaltOnce sync.Once
	auditSalt     atomic.Pointer[[]byte]
)

// SetAuditSalt allows tests (or an operator with a fixed-salt requirement)
// to install a deterministic salt for the PII hasher. In production, leave
// this unset so a random salt is generated lazily.
func SetAuditSalt(salt []byte) {
	cp := append([]byte(nil), salt...)
	auditSalt.Store(&cp)
}

func getAuditSalt() []byte {
	auditSaltOnce.Do(func() {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			// Degrade to an empty salt rather than panicking; the hash
			// still provides some obfuscation and we refuse to crash
			// because of log-line instrumentation.
			buf = []byte{}
		}
		auditSalt.CompareAndSwap(nil, &buf)
	})
	p := auditSalt.Load()
	if p == nil {
		return nil
	}
	return *p
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

// hashClientIP resolves the client IP via internal/clientip (so forwarded
// headers are honoured when manager has trusted proxies configured) and
// returns the salted hash. Falls back to the raw RemoteAddr when manager
// is nil so this still works in test paths that bypass full wiring.
func hashClientIP(manager *Manager, r *http.Request) string {
	if r == nil {
		return ""
	}
	if manager != nil {
		if ip := clientip.ExtractString(r, manager.TrustedProxies()); ip != "" {
			return hashRemoteAddr(ip)
		}
	}
	return hashRemoteAddr(r.RemoteAddr)
}

// denyUnauthenticated returns an *UnauthenticatedError naming the default
// scheme and the manager's login target, and writes nothing: the error
// pipeline answers it (see Manager.RenderUnauthenticated). Shared by all
// auth-requiring middleware. The manager's logger records the denial when
// installed.
func denyUnauthenticated(manager *Manager, c *router.Context) error {
	manager.logWarn("velocity/auth: authentication required", "method", c.Request.Method, "path", c.Request.URL.Path, "ip_hash", hashClientIP(manager, c.Request))
	// Intended stash: remember the originally requested URL server-side in
	// the session, so the browser is bounced to a clean login target. The
	// URL bar never exposes the destination and an attacker cannot inject
	// one via ?redirect=. ctx.RedirectToIntended pulls it back after login.
	//
	// Only stash safe GET navigations that are not JSON requests (Inertia
	// visits are GET). A POST/PUT that lands here lost its body to the
	// redirect anyway, and stashing it would replay the wrong intent after
	// login. The session cookie is set now, before anything is written, so
	// it rides on whatever response the pipeline renders.
	if c.Request.Method == http.MethodGet && !c.WantsJSON() {
		if sess := manager.Session(c.Request); sess != nil {
			redirectURL := c.Request.URL.Path
			if c.Request.URL.RawQuery != "" {
				redirectURL += "?" + c.Request.URL.RawQuery
			}
			sess.Put(router.IntendedSessionKey, redirectURL)
			_ = sess.Save(c.Response)
		}
	}
	return &UnauthenticatedError{
		Schemes:    []string{manager.defaultSchemeName()},
		RedirectTo: manager.loginTarget(c.Request),
	}
}

// denyForbidden returns a *ForbiddenError and writes nothing: the error
// pipeline answers it with a 403 (an Inertia request gets the pipeline's
// Inertia answer). The manager's logger records the denial when installed.
func denyForbidden(manager *Manager, c *router.Context) error {
	manager.logWarn("velocity/auth: authorization denied", "method", c.Request.Method, "path", c.Request.URL.Path, "ip_hash", hashClientIP(manager, c.Request))
	return &ForbiddenError{}
}

// FromContext extracts the *Manager from a router.Context.
// Returns nil if auth is not configured, including when the context has
// no service container at all (e.g. a bare test context), so callers
// can rely on the documented nil contract instead of a panic.
func FromContext(ctx *router.Context) *Manager {
	s := ctx.ServicesIfSet()
	if s == nil || s.Auth == nil {
		return nil
	}
	m, _ := s.Auth.(*Manager)
	return m
}

// FromServices extracts the *Manager from a service container, which is
// what a Module's Init/Start receives. It is the Services-side
// counterpart to [FromContext]: app.Services types the field as
// contract.AuthManager (contract is a stdlib-only leaf and cannot name
// auth's types), so a module that needs the concrete manager - to install
// its own user store, for instance - goes through here.
//
// Returns nil when auth is not configured, so callers get the documented
// nil contract rather than a panic.
//
//	func (p *AppModule) Init(s *velocity.Services) error {
//	    manager := auth.FromServices(s)
//	    if manager == nil {
//	        return errors.New("auth is not configured")
//	    }
//	    manager.SetUserStore(ormauth.New[models.User]())
//	    return nil
//	}
func FromServices(s *app.Services) *Manager {
	if s == nil || s.Auth == nil {
		return nil
	}
	m, _ := s.Auth.(*Manager)
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
			if !manager.Access().HasRole(user, role) {
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
			if !manager.Access().HasAnyRole(user, roles...) {
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
			if !manager.Access().HasAllRoles(user, roles...) {
				return denyForbidden(manager, c)
			}
			return next(c)
		}
	}
}

// AuthorizeMiddleware returns middleware that checks if the authenticated user
// can perform the given ability. An optional resourceFunc can provide the
// resource argument for the access check. Returns 401 if not authenticated, 403
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
				allowed = manager.Access().Allows(user, ability, resource)
			} else {
				allowed = manager.Access().Allows(user, ability)
			}
			if !allowed {
				return denyForbidden(manager, c)
			}
			return next(c)
		}
	}
}

// GuestMiddleware returns middleware that only allows unauthenticated users.
// For an authenticated user it returns a 403 error for a request that
// wants JSON, and redirects any other request to "/".
func GuestMiddleware(manager *Manager) router.MiddlewareFunc {
	return GuestMiddlewareWithRedirect(manager, "/")
}

// GuestMiddlewareWithRedirect returns middleware that only allows
// unauthenticated users. For an authenticated user it returns a 403 error
// ("Already authenticated.") for a request that wants JSON, rendered by the
// error pipeline; any other request, Inertia included, is redirected (303)
// to redirectTo and contract.ErrResponseWritten is returned.
func GuestMiddlewareWithRedirect(manager *Manager, redirectTo string) router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			if manager.Check(c.Request) {
				if c.WantsJSON() {
					return contract.NewHTTPError(http.StatusForbidden, "Already authenticated.")
				}
				// Browser/Inertia: redirect (Inertia follows as a fresh visit).
				if err := c.Redirect(http.StatusSeeOther, redirectTo); err != nil {
					return err
				}
				return contract.ErrResponseWritten
			}
			return next(c)
		}
	}
}
