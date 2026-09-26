package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
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
		{name: "inertia redirected", err: &UnauthenticatedError{RedirectTo: "/login"}, kind: kindInertia, want: true, wantLocation: "/login"},
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

// facetScheme is a scheme implementing StatelessScheme and
// ChallengeScheme with fixed answers.
type facetScheme struct {
	mockSchemeForMiddleware
	stateless bool
	challenge string
}

func (s *facetScheme) Stateless() bool   { return s.stateless }
func (s *facetScheme) Challenge() string { return s.challenge }

// challengeOnlyScheme implements ChallengeScheme and not StatelessScheme.
type challengeOnlyScheme struct {
	mockSchemeForMiddleware
	challenge string
}

func (s *challengeOnlyScheme) Challenge() string { return s.challenge }

// newSchemeManager returns a manager with def as the default scheme and
// these schemes: "api" (stateless, challenges "Bearer", like the JWT
// scheme), "api2" (the same), "basic" (stateless, challenges
// `Basic realm="app"`), "evil" (stateless, a challenge carrying CRLF),
// "hmac" (challenges "HMAC-SHA256", not a StatelessScheme), "web"
// (session-aware, no challenge) and "half" (Stateless reports false, no
// challenge).
func newSchemeManager(def string) *Manager {
	m := NewManager()
	m.RegisterScheme("api", &facetScheme{stateless: true, challenge: "Bearer"})
	m.RegisterScheme("api2", &facetScheme{stateless: true, challenge: "Bearer"})
	m.RegisterScheme("basic", &facetScheme{stateless: true, challenge: `Basic realm="app"`})
	m.RegisterScheme("evil", &facetScheme{stateless: true, challenge: "Bearer\r\nX-Evil: 1"})
	m.RegisterScheme("hmac", &challengeOnlyScheme{challenge: "HMAC-SHA256"})
	m.RegisterScheme("web", &lookupCountingScheme{})
	m.RegisterScheme("half", &facetScheme{stateless: false})
	m.SetDefaultScheme(def)
	return m
}

// TestManager_RenderUnauthenticated_Challenges asserts the render rule
// adds one WWW-Authenticate line per checked scheme with a challenge, in
// order and once each, on every path that hands the 401 back to the
// pipeline, and none on a 303, for a session-only denial or for a value
// holding CRLF. The challenge follows ChallengeScheme alone: a scheme
// that challenges without being stateless keeps the redirect for a
// browser and still sends its challenge on every 401 path.
func TestManager_RenderUnauthenticated_Challenges(t *testing.T) {
	tests := []struct {
		name        string
		schemes     []string
		redirectTo  string
		kind        string
		wantRender  bool
		wantHeaders []string
	}{
		{name: "jwt only json", schemes: []string{"api"}, kind: kindJSON, wantHeaders: []string{"Bearer"}},
		{name: "jwt only browser", schemes: []string{"api"}, kind: kindBrowser, wantHeaders: []string{"Bearer"}},
		{name: "jwt only inertia", schemes: []string{"api"}, kind: kindInertia, wantHeaders: []string{"Bearer"}},
		{name: "scheme challenge value", schemes: []string{"basic"}, kind: kindJSON, wantHeaders: []string{`Basic realm="app"`}},
		{name: "two challenges in order", schemes: []string{"api", "basic"}, kind: kindJSON, wantHeaders: []string{"Bearer", `Basic realm="app"`}},
		{name: "two challenges reversed", schemes: []string{"basic", "api"}, kind: kindJSON, wantHeaders: []string{`Basic realm="app"`, "Bearer"}},
		{name: "same challenge once", schemes: []string{"api", "api2"}, kind: kindJSON, wantHeaders: []string{"Bearer"}},
		{name: "crlf challenge dropped", schemes: []string{"evil"}, kind: kindJSON},
		{name: "crlf dropped beside a valid one", schemes: []string{"evil", "api"}, kind: kindJSON, wantHeaders: []string{"Bearer"}},
		{name: "session only json", schemes: []string{"web"}, kind: kindJSON},
		{name: "session only browser redirected", schemes: []string{"web"}, kind: kindBrowser, wantRender: true},
		{name: "mixed browser redirected without challenge", schemes: []string{"api", "web"}, kind: kindBrowser, wantRender: true},
		{name: "mixed json", schemes: []string{"api", "web"}, kind: kindJSON, wantHeaders: []string{"Bearer"}},
		{name: "refused redirect challenges", schemes: []string{"api", "web"}, redirectTo: "https://evil.example/login", kind: kindBrowser, wantHeaders: []string{"Bearer"}},
		{name: "unknown scheme", schemes: []string{"missing"}, kind: kindJSON},
		{name: "non-stateless challenge json", schemes: []string{"hmac"}, kind: kindJSON, wantHeaders: []string{"HMAC-SHA256"}},
		{name: "non-stateless challenge browser redirected", schemes: []string{"hmac"}, kind: kindBrowser, wantRender: true},
		{name: "non-stateless challenge refused redirect", schemes: []string{"hmac"}, redirectTo: "https://evil.example/login", kind: kindBrowser, wantHeaders: []string{"HMAC-SHA256"}},
		{name: "non-stateless challenge beside jwt json", schemes: []string{"hmac", "api"}, kind: kindJSON, wantHeaders: []string{"HMAC-SHA256", "Bearer"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newSchemeManager("web")
			err := &UnauthenticatedError{Schemes: tt.schemes, RedirectTo: tt.redirectTo}
			w := &writeTracker{ResponseRecorder: httptest.NewRecorder()}
			rc := contract.NewRenderContext(w, newDenialRequest(http.MethodGet, "/dashboard", tt.kind))

			if got := m.RenderUnauthenticated(rc, err, nil); got != tt.wantRender {
				t.Fatalf("RenderUnauthenticated = %v, want %v", got, tt.wantRender)
			}
			if tt.wantRender && w.Code != http.StatusSeeOther {
				t.Errorf("status = %d, want 303", w.Code)
			}
			if got := w.Header().Values("WWW-Authenticate"); !slices.Equal(got, tt.wantHeaders) {
				t.Errorf("WWW-Authenticate = %q, want %q", got, tt.wantHeaders)
			}
		})
	}
}

// TestManager_RenderUnauthenticated_StatelessSchemes asserts a request
// denied only by stateless schemes is never redirected, whatever it
// accepts, while a session or unknown scheme among the checked ones keeps
// the redirect. The schemes resolve through the manager carried on the
// error, else the receiver.
func TestManager_RenderUnauthenticated_StatelessSchemes(t *testing.T) {
	sessionAPI := NewManager()
	sessionAPI.RegisterScheme("api", &lookupCountingScheme{})
	tests := []struct {
		name       string
		nilManager bool
		schemes    []string
		errManager *Manager
		kind       string
		want       bool
	}{
		{name: "stateless only", schemes: []string{"api"}, kind: kindBrowser},
		{name: "stateless only inertia", schemes: []string{"api"}, kind: kindInertia},
		{name: "session only", schemes: []string{"web"}, kind: kindBrowser, want: true},
		{name: "stateless and session", schemes: []string{"api", "web"}, kind: kindBrowser, want: true},
		{name: "session and stateless", schemes: []string{"web", "api"}, kind: kindBrowser, want: true},
		{name: "unknown scheme", schemes: []string{"missing"}, kind: kindBrowser, want: true},
		{name: "stateless and unknown", schemes: []string{"api", "missing"}, kind: kindBrowser, want: true},
		{name: "stateless reports false", schemes: []string{"half"}, kind: kindBrowser, want: true},
		{name: "no scheme named", kind: kindBrowser, want: true},
		{name: "nil manager resolves nothing", nilManager: true, schemes: []string{"api"}, kind: kindBrowser, want: true},
		{name: "error manager stateless", schemes: []string{"api"}, errManager: newSchemeManager("api"), kind: kindBrowser},
		{name: "error manager session outranks receiver", schemes: []string{"api"}, errManager: sessionAPI, kind: kindBrowser, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m *Manager
			if !tt.nilManager {
				m = newSchemeManager("web")
			}
			err := &UnauthenticatedError{Schemes: tt.schemes, manager: tt.errManager}
			w := &writeTracker{ResponseRecorder: httptest.NewRecorder()}
			rc := contract.NewRenderContext(w, newDenialRequest(http.MethodGet, "/dashboard", tt.kind))

			if got := m.RenderUnauthenticated(rc, err, nil); got != tt.want {
				t.Fatalf("RenderUnauthenticated = %v, want %v", got, tt.want)
			}
			if !tt.want {
				if w.wrote || w.Header().Get("Location") != "" {
					t.Errorf("fall-through wrote a response (code %d, Location %q)", w.Code, w.Header().Get("Location"))
				}
				return
			}
			if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
				t.Errorf("response = %d %q, want 303 /login", w.Code, w.Header().Get("Location"))
			}
		})
	}
}

// TestAuthMiddleware_StatelessDenialThroughPipeline drives an auth guard
// denial through the router boundary and the error pipeline: a browser
// request denied by a stateless default scheme answers 401 with no
// Location, one denied by a session scheme is redirected, and the
// scheme's challenge survives the HTML and problem+json renders.
func TestAuthMiddleware_StatelessDenialThroughPipeline(t *testing.T) {
	tests := []struct {
		name          string
		scheme        string
		kind          string
		wantStatus    int
		wantLocation  string
		wantType      string
		wantChallenge []string
	}{
		{name: "stateless browser", scheme: "api", kind: kindBrowser, wantStatus: http.StatusUnauthorized, wantType: "text/html", wantChallenge: []string{"Bearer"}},
		{name: "stateless json", scheme: "api", kind: kindJSON, wantStatus: http.StatusUnauthorized, wantType: problem.ProblemTypeContent, wantChallenge: []string{"Bearer"}},
		{name: "session browser", scheme: "web", kind: kindBrowser, wantStatus: http.StatusSeeOther, wantLocation: "/login"},
		{name: "session json", scheme: "web", kind: kindJSON, wantStatus: http.StatusUnauthorized, wantType: problem.ProblemTypeContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newSchemeManager(tt.scheme)
			w, rep := servePipeline(t, m, AuthMiddleware(m), newDenialRequest(http.MethodGet, "/dashboard", tt.kind))

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			if got := w.Header().Get("Location"); got != tt.wantLocation {
				t.Errorf("Location = %q, want %q", got, tt.wantLocation)
			}
			if got := w.Header().Get("Content-Type"); tt.wantType != "" && !strings.HasPrefix(got, tt.wantType) {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantType)
			}
			if got := w.Header().Values("WWW-Authenticate"); !slices.Equal(got, tt.wantChallenge) {
				t.Errorf("WWW-Authenticate = %q, want %q", got, tt.wantChallenge)
			}
			if rep.count() != 0 {
				t.Errorf("reports = %d, want 0", rep.count())
			}
		})
	}
}

// TestRenderUnauthenticated_RefusedRedirectChallengesThroughPipeline
// asserts a denial checked by a stateless and a session scheme, whose
// unsafe RedirectTo the render context refuses, answers the pipeline's
// 401 carrying the stateless scheme's challenge.
func TestRenderUnauthenticated_RefusedRedirectChallengesThroughPipeline(t *testing.T) {
	m := newSchemeManager("web")
	deny := func(router.HandlerFunc) router.HandlerFunc {
		return func(*router.Context) error {
			return &UnauthenticatedError{Schemes: []string{"api", "web"}, RedirectTo: "https://evil.example/login", manager: m}
		}
	}
	w, rep := servePipeline(t, m, deny, newDenialRequest(http.MethodGet, "/dashboard", kindBrowser))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (Location %q)", w.Code, w.Header().Get("Location"))
	}
	if got := w.Header().Get("Location"); got != "" {
		t.Errorf("Location = %q, want none", got)
	}
	if got := w.Header().Values("WWW-Authenticate"); !slices.Equal(got, []string{"Bearer"}) {
		t.Errorf("WWW-Authenticate = %q, want [Bearer]", got)
	}
	if rep.count() != 0 {
		t.Errorf("reports = %d, want 0", rep.count())
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
			s.Put(UserIDSessionKey, id)
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

// TestManager_RenderAlreadyAuthenticated asserts the guest render rule:
// JSON falls through to the 403 body, a browser or Inertia request is
// redirected to the error's target ("/" when empty), and a refused target
// warns and falls through.
func TestManager_RenderAlreadyAuthenticated(t *testing.T) {
	tests := []struct {
		name         string
		nilManager   bool
		err          error
		kind         string
		want         bool
		wantLocation string
		wantWarn     bool
	}{
		{name: "json falls through", err: &AlreadyAuthenticatedError{RedirectTo: "/home"}, kind: kindJSON},
		{name: "xhr falls through", err: &AlreadyAuthenticatedError{RedirectTo: "/home"}, kind: kindXHR},
		{name: "browser redirected", err: &AlreadyAuthenticatedError{RedirectTo: "/home"}, kind: kindBrowser, want: true, wantLocation: "/home"},
		{name: "inertia redirected", err: &AlreadyAuthenticatedError{RedirectTo: "/home"}, kind: kindInertia, want: true, wantLocation: "/home"},
		{name: "wrapped error target", err: fmt.Errorf("guard: %w", &AlreadyAuthenticatedError{RedirectTo: "/home"}), kind: kindBrowser, want: true, wantLocation: "/home"},
		{name: "empty target is root", err: &AlreadyAuthenticatedError{}, kind: kindBrowser, want: true, wantLocation: "/"},
		{name: "nil manager", nilManager: true, err: &AlreadyAuthenticatedError{}, kind: kindBrowser, want: true, wantLocation: "/"},
		{name: "protocol relative refused", err: &AlreadyAuthenticatedError{RedirectTo: "//evil"}, kind: kindBrowser, wantWarn: true},
		{name: "absolute refused", err: &AlreadyAuthenticatedError{RedirectTo: "https://evil.example/"}, kind: kindBrowser, wantWarn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &warnRecorder{}
			var m *Manager
			if !tt.nilManager {
				m = NewManager()
				m.SetLogger(logs)
			}
			w := &writeTracker{ResponseRecorder: httptest.NewRecorder()}
			rc := contract.NewRenderContext(w, newDenialRequest(http.MethodGet, "/login", tt.kind))

			if got := m.RenderAlreadyAuthenticated(rc, tt.err, nil); got != tt.want {
				t.Fatalf("RenderAlreadyAuthenticated = %v, want %v", got, tt.want)
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
	if nilManager.RenderAlreadyAuthenticated(nil, &AlreadyAuthenticatedError{}, nil) {
		t.Error("RenderAlreadyAuthenticated with a nil render context = true, want false")
	}
}
