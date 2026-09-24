package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

var _ contract.RequestUserIdentifier = (*Manager)(nil)

// TestUnauthenticatedError asserts the error text, the 401 status and
// that it is never reported.
func TestUnauthenticatedError(t *testing.T) {
	tests := []struct {
		name    string
		err     *UnauthenticatedError
		wantMsg string
	}{
		{name: "no schemes", err: &UnauthenticatedError{}, wantMsg: "velocity/auth: unauthenticated"},
		{name: "one scheme", err: &UnauthenticatedError{Schemes: []string{"web"}}, wantMsg: "velocity/auth: unauthenticated (schemes: web)"},
		{name: "two schemes", err: &UnauthenticatedError{Schemes: []string{"web", "api"}}, wantMsg: "velocity/auth: unauthenticated (schemes: web, api)"},
		{name: "nil", err: nil, wantMsg: "velocity/auth: unauthenticated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
			if tt.err.StatusCode() != http.StatusUnauthorized {
				t.Errorf("StatusCode() = %d, want 401", tt.err.StatusCode())
			}
			if tt.err.ShouldReport() {
				t.Error("ShouldReport() = true, want false")
			}
			if tt.err == nil {
				return
			}
			status, _, ok := contract.StatusOf(fmt.Errorf("wrapped: %w", tt.err))
			if !ok || status != http.StatusUnauthorized {
				t.Errorf("StatusOf = %d, %v; want 401, true", status, ok)
			}
		})
	}
}

// TestForbiddenError asserts the 403 status, the unwrap chain, and that
// errors.Is reaches ErrUnauthorized whatever Err holds.
func TestForbiddenError(t *testing.T) {
	cause := errors.New("policy said no")
	tests := []struct {
		name       string
		err        *ForbiddenError
		wantMsg    string
		wantUnwrap error
	}{
		{name: "no cause", err: &ForbiddenError{}, wantMsg: "velocity/auth: forbidden", wantUnwrap: ErrUnauthorized},
		{name: "sentinel cause", err: &ForbiddenError{Err: ErrUnauthorized}, wantMsg: "velocity/auth: forbidden: unauthorized action", wantUnwrap: ErrUnauthorized},
		{name: "own cause", err: &ForbiddenError{Err: cause}, wantMsg: "velocity/auth: forbidden: policy said no", wantUnwrap: cause},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
			if got := tt.err.Unwrap(); got != tt.wantUnwrap {
				t.Errorf("Unwrap() = %v, want %v", got, tt.wantUnwrap)
			}
			wrapped := fmt.Errorf("handler: %w", tt.err)
			if !errors.Is(wrapped, ErrUnauthorized) {
				t.Error("errors.Is(err, ErrUnauthorized) = false, want true")
			}
			if !errors.Is(wrapped, tt.wantUnwrap) {
				t.Errorf("errors.Is(err, %v) = false, want true", tt.wantUnwrap)
			}
			if errors.Is(wrapped, ErrPolicyNotFound) {
				t.Error("errors.Is(err, ErrPolicyNotFound) = true, want false")
			}
			status, _, ok := contract.StatusOf(wrapped)
			if !ok || status != http.StatusForbidden {
				t.Errorf("StatusOf = %d, %v; want 403, true", status, ok)
			}
			if tt.err.ShouldReport() {
				t.Error("ShouldReport() = true, want false")
			}
		})
	}
	var nilErr *ForbiddenError
	if nilErr.Error() != "velocity/auth: forbidden" || nilErr.Unwrap() != ErrUnauthorized {
		t.Errorf("nil ForbiddenError = %q, %v", nilErr.Error(), nilErr.Unwrap())
	}
}

// TestManager_SetLoginRedirect asserts the login target resolution.
func TestManager_SetLoginRedirect(t *testing.T) {
	tests := []struct {
		name string
		set  bool
		fn   func(*http.Request) string
		want string
	}{
		{name: "default", want: "/login"},
		{name: "configured", set: true, fn: func(*http.Request) string { return "/auth/sign-in" }, want: "/auth/sign-in"},
		{name: "per request", set: true, fn: func(r *http.Request) string { return "/" + r.Host + "/login" }, want: "/example.com/login"},
		{name: "empty answer", set: true, fn: func(*http.Request) string { return "" }, want: "/login"},
		{name: "reset with nil", set: true, fn: nil, want: "/login"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManager()
			m.SetLoginRedirect(func(*http.Request) string { return "/stale" })
			if tt.set {
				m.SetLoginRedirect(tt.fn)
			} else {
				m.SetLoginRedirect(nil)
			}
			if got := m.loginTarget(httptest.NewRequest(http.MethodGet, "/x", nil)); got != tt.want {
				t.Errorf("loginTarget = %q, want %q", got, tt.want)
			}
		})
	}
	var nilManager *Manager
	if got := nilManager.loginTarget(nil); got != "/login" {
		t.Errorf("nil manager loginTarget = %q, want /login", got)
	}
}

// warnRecorder is an auth Logger that records warnings.
type warnRecorder struct {
	mu    sync.Mutex
	warns []string
}

func (l *warnRecorder) Info(string, ...any)  {}
func (l *warnRecorder) Error(string, ...any) {}
func (l *warnRecorder) Warn(msg string, _ ...any) {
	l.mu.Lock()
	l.warns = append(l.warns, msg)
	l.mu.Unlock()
}

// TestManager_RenderUnauthenticated drives the render rule body through
// the bare net/http render context.
func TestManager_RenderUnauthenticated(t *testing.T) {
	tests := []struct {
		name         string
		nilManager   bool
		loginTarget  string
		err          error
		kind         string
		want         bool
		wantLocation string
		wantWarn     bool
	}{
		{name: "json falls through", err: &UnauthenticatedError{RedirectTo: "/login"}, kind: kindJSON},
		{name: "xhr falls through", err: &UnauthenticatedError{}, kind: kindXHR},
		{name: "inertia falls through", err: &UnauthenticatedError{RedirectTo: "/login"}, kind: kindInertia},
		{name: "error target wins", loginTarget: "/sign-in", err: &UnauthenticatedError{RedirectTo: "/custom"}, kind: kindBrowser, want: true, wantLocation: "/custom"},
		{name: "wrapped error target", err: fmt.Errorf("guard: %w", &UnauthenticatedError{RedirectTo: "/custom"}), kind: kindBrowser, want: true, wantLocation: "/custom"},
		{name: "manager target", loginTarget: "/sign-in", err: &UnauthenticatedError{}, kind: kindBrowser, want: true, wantLocation: "/sign-in"},
		{name: "default target", err: &UnauthenticatedError{}, kind: kindBrowser, want: true, wantLocation: "/login"},
		{name: "nil manager", nilManager: true, err: &UnauthenticatedError{}, kind: kindBrowser, want: true, wantLocation: "/login"},
		{name: "unsafe target refused", err: &UnauthenticatedError{RedirectTo: "https://evil.example/login"}, kind: kindBrowser, wantWarn: true},
		{name: "protocol relative refused", loginTarget: "//evil.example", err: &UnauthenticatedError{}, kind: kindBrowser, wantWarn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &warnRecorder{}
			var m *Manager
			if !tt.nilManager {
				m = NewManager()
				m.SetLogger(logs)
				if tt.loginTarget != "" {
					target := tt.loginTarget
					m.SetLoginRedirect(func(*http.Request) string { return target })
				}
			}
			w := &writeTracker{ResponseRecorder: httptest.NewRecorder()}
			rc := contract.NewRenderContext(w, newDenialRequest(http.MethodGet, "/dashboard", tt.kind))

			if got := m.RenderUnauthenticated(rc, tt.err, nil); got != tt.want {
				t.Fatalf("RenderUnauthenticated = %v, want %v", got, tt.want)
			}
			if !tt.want {
				if w.wrote || w.Header().Get("Location") != "" {
					t.Errorf("fall-through wrote a response (code %d, Location %q)", w.Code, w.Header().Get("Location"))
				}
			} else if w.Code != http.StatusSeeOther || w.Header().Get("Location") != tt.wantLocation {
				t.Errorf("response = %d %q, want 303 %q", w.Code, w.Header().Get("Location"), tt.wantLocation)
			}
			if gotWarn := len(logs.warns) > 0; gotWarn != tt.wantWarn {
				t.Errorf("warned = %v (%v), want %v", gotWarn, logs.warns, tt.wantWarn)
			}
		})
	}
	var nilManager *Manager
	if nilManager.RenderUnauthenticated(nil, &UnauthenticatedError{}, nil) {
		t.Error("RenderUnauthenticated with a nil render context = true, want false")
	}
}

// idScheme is a scheme that is not SessionAware and answers ID with id,
// or panics when boom is set.
type idScheme struct {
	mockSchemeForMiddleware
	id   interface{}
	boom bool
}

func (s *idScheme) ID(*http.Request) interface{} {
	if s.boom {
		panic("scheme exploded")
	}
	return s.id
}

// lookupCountingScheme is a SessionAware scheme that counts user lookups,
// so a test can assert RequestUserID never resolves the user.
type lookupCountingScheme struct {
	mockSchemeForMiddleware
	sess    Session
	lookups int
}

func (s *lookupCountingScheme) Session(*http.Request) Session { return s.sess }

func (s *lookupCountingScheme) User(*http.Request) Authenticatable {
	s.lookups++
	return nil
}

func (s *lookupCountingScheme) ID(*http.Request) interface{} {
	s.lookups++
	return nil
}

// TestManager_RequestUserID asserts the user id named for error reports,
// read from the session without a user lookup for session schemes.
func TestManager_RequestUserID(t *testing.T) {
	sessionWith := func(id interface{}) Session {
		s := NewSession("sid")
		if id != nil {
			s.Put(sessionUserIDKey, id)
		}
		return s
	}
	tests := []struct {
		name       string
		nilManager bool
		nilRequest bool
		scheme     Scheme
		want       string
	}{
		{name: "nil manager", nilManager: true},
		{name: "nil request", nilRequest: true, scheme: &idScheme{id: "u-1"}},
		{name: "no default scheme"},
		{name: "session with string id", scheme: &lookupCountingScheme{sess: sessionWith("u-1")}, want: "u-1"},
		{name: "session with numeric id", scheme: &lookupCountingScheme{sess: sessionWith(42)}, want: "42"},
		{name: "session without user", scheme: &lookupCountingScheme{sess: sessionWith(nil)}},
		{name: "no session", scheme: &lookupCountingScheme{}},
		{name: "scheme id", scheme: &idScheme{id: int64(7)}, want: "7"},
		{name: "scheme without user", scheme: &idScheme{}},
		{name: "panicking scheme", scheme: &idScheme{boom: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m *Manager
			if !tt.nilManager {
				m = NewManager()
				if tt.scheme != nil {
					m.RegisterScheme("web", tt.scheme)
				}
			}
			var r *http.Request
			if !tt.nilRequest {
				r = httptest.NewRequest(http.MethodGet, "/x", nil)
			}
			if got := m.RequestUserID(r); got != tt.want {
				t.Errorf("RequestUserID = %q, want %q", got, tt.want)
			}
			if s, ok := tt.scheme.(*lookupCountingScheme); ok && s.lookups != 0 {
				t.Errorf("user lookups = %d, want 0", s.lookups)
			}
		})
	}
}
