package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/velocitykode/velocity/contract"
)

// defaultLoginPath is the login target used when no login redirect is set.
const defaultLoginPath = "/login"

// sessionUserIDKey is the session key the session scheme anchors the
// authenticated user's id under.
const sessionUserIDKey = "user_id"

// UnauthenticatedError is returned when a request needs an authenticated
// user and has none. It answers 401 and is not reported. The framework's
// default render rule answers a request that wants JSON with a 401
// problem+json body, an Inertia request through the error pipeline's
// Inertia branch, and any other request with a redirect to the login
// target (see Manager.RenderUnauthenticated).
type UnauthenticatedError struct {
	// Schemes names the authentication schemes that were checked.
	Schemes []string
	// RedirectTo is the login target for a browser request. Empty means
	// the auth manager's login redirect.
	RedirectTo string
}

// Error returns a message naming the checked schemes.
func (e *UnauthenticatedError) Error() string {
	if e == nil || len(e.Schemes) == 0 {
		return "velocity/auth: unauthenticated"
	}
	return "velocity/auth: unauthenticated (schemes: " + strings.Join(e.Schemes, ", ") + ")"
}

// StatusCode returns 401.
func (e *UnauthenticatedError) StatusCode() int { return http.StatusUnauthorized }

// ShouldReport returns false: a missing login is a client outcome.
func (e *UnauthenticatedError) ShouldReport() bool { return false }

// ForbiddenError is returned when an authenticated user may not perform
// the request. It answers 403, is not reported, and matches ErrUnauthorized
// under errors.Is whatever Err holds.
type ForbiddenError struct {
	// Err is the underlying denial. Nil means ErrUnauthorized.
	Err error
}

// Error returns the denial message, with Err's text when set.
func (e *ForbiddenError) Error() string {
	if e == nil || e.Err == nil {
		return "velocity/auth: forbidden"
	}
	return "velocity/auth: forbidden: " + e.Err.Error()
}

// StatusCode returns 403.
func (e *ForbiddenError) StatusCode() int { return http.StatusForbidden }

// ShouldReport returns false: a denial is a client outcome.
func (e *ForbiddenError) ShouldReport() bool { return false }

// Unwrap returns Err, or ErrUnauthorized when Err is nil.
func (e *ForbiddenError) Unwrap() error {
	if e == nil || e.Err == nil {
		return ErrUnauthorized
	}
	return e.Err
}

// Is reports whether target is ErrUnauthorized, so a ForbiddenError
// carrying its own Err still matches the authorization sentinel.
func (e *ForbiddenError) Is(target error) bool {
	return target == ErrUnauthorized
}

// loginRedirectHolder boxes the login redirect function so atomic.Pointer
// can hold it as a single addressable cell.
type loginRedirectHolder struct {
	fn func(*http.Request) string
}

// SetLoginRedirect sets the function naming the login target an
// unauthenticated browser request is redirected to. An empty result, or a
// nil fn, means the default "/login". The target must be a same-origin
// path or a host allowed by the router's redirect allowlist; any other
// target is refused when the redirect is written.
func (m *Manager) SetLoginRedirect(fn func(*http.Request) string) {
	if fn == nil {
		m.loginRedirect.Store(nil)
		return
	}
	m.loginRedirect.Store(&loginRedirectHolder{fn: fn})
}

// loginTarget returns the login target for r: the login redirect's answer
// when it names one, else "/login". A nil manager returns "/login".
func (m *Manager) loginTarget(r *http.Request) string {
	if m != nil {
		if h := m.loginRedirect.Load(); h != nil {
			if target := h.fn(r); target != "" {
				return target
			}
		}
	}
	return defaultLoginPath
}

// defaultSchemeName returns the name of the default scheme.
func (m *Manager) defaultSchemeName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.defaultScheme
}

// RequestUserID names the authenticated user of r through the default
// scheme, or "" when there is none. It implements
// contract.RequestUserIdentifier, so error reports carry the user id.
//
// A session scheme answers from the user id stored in the request's
// session, without a user store lookup, so reporting a failed request
// never queries the database or revives a remember cookie; any other
// scheme answers through its ID. A nil manager, a missing scheme and a
// panicking scheme all yield "".
func (m *Manager) RequestUserID(r *http.Request) (id string) {
	if m == nil || r == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			id = ""
		}
	}()
	scheme, err := m.DefaultScheme()
	if err != nil {
		return ""
	}
	var raw any
	if sa, ok := scheme.(SessionAware); ok {
		sess := sa.Session(r)
		if sess == nil {
			return ""
		}
		raw = sess.Get(sessionUserIDKey)
	} else {
		raw = scheme.ID(r)
	}
	if raw == nil {
		return ""
	}
	if s, ok := raw.(string); ok {
		return s
	}
	return fmt.Sprint(raw)
}

// RenderUnauthenticated is the framework's default render rule for an
// *UnauthenticatedError in err's chain. A request that wants JSON, and an
// Inertia request, return false so the error pipeline renders the 401
// problem+json body or its Inertia answer. Any other request is
// redirected (303) to the error's RedirectTo, or to the manager's login
// target when that is empty, and true is returned. A target the render
// context refuses (not same-origin and not an allowed host) is logged and
// returns false, leaving the 401 to the pipeline. A nil manager uses
// "/login".
func (m *Manager) RenderUnauthenticated(rc contract.RenderContext, err error, _ *contract.ErrorContext) bool {
	if rc == nil || rc.WantsJSON() || rc.IsInertia() {
		return false
	}
	target := ""
	var ue *UnauthenticatedError
	if errors.As(err, &ue) {
		target = ue.RedirectTo
	}
	if target == "" {
		target = m.loginTarget(rc.Request())
	}
	if redirectErr := rc.Redirect(http.StatusSeeOther, target); redirectErr != nil {
		if m != nil {
			m.logWarn("velocity/auth: login redirect refused", "error", redirectErr.Error())
		}
		return false
	}
	return true
}
