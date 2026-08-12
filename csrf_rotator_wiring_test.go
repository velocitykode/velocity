package velocity

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/contract"
)

// fakeCSRFRotator is a contract.CSRFProtector + contract.CSRFTokenRotator
// pair used to spot whether the framework's bootstrap installed THIS
// instance on auth.Manager or the original framework-built CSRF. The
// identifying field `tag` is captured by recorderScheme.SetCSRFTokenRotator
// after RegisterScheme runs the rotator through the propagation path.
type fakeCSRFRotator struct {
	tag string
}

func (f *fakeCSRFRotator) Middleware(next http.Handler) http.Handler { return next }
func (f *fakeCSRFRotator) RotateToken(oldID, newID string) error     { return nil }
func (f *fakeCSRFRotator) RevokeToken(id string) error               { return nil }
func (f *fakeCSRFRotator) WriteXSRFCookie(_ http.ResponseWriter, _ string) {
}
func (f *fakeCSRFRotator) ClearXSRFCookie(_ http.ResponseWriter, _ *http.Request) {
}

// compile-time check: fakeCSRFRotator satisfies BOTH the contract used to
// store CSRF on Services AND the rotator capability the auth manager
// looks for.
var (
	_ contract.CSRFProtector    = (*fakeCSRFRotator)(nil)
	_ contract.CSRFTokenRotator = (*fakeCSRFRotator)(nil)
)

// recorderScheme is a minimal auth.Scheme that captures the rotator passed
// to it via the CSRFTokenRotatorReceiver propagation when RegisterScheme
// runs. Every other Scheme method is a no-op; only Login is exercised by
// the rest of the test surface (it returns an error so any caller fails
// fast if it somehow does run).
type recorderScheme struct {
	mu          sync.Mutex
	got         contract.CSRFTokenRotator
	setCallsLen int
}

func (g *recorderScheme) SetCSRFTokenRotator(r contract.CSRFTokenRotator) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.got = r
	g.setCallsLen++
}

func (g *recorderScheme) lastRotator() contract.CSRFTokenRotator {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.got
}

func (g *recorderScheme) Check(_ *http.Request) bool                { return false }
func (g *recorderScheme) User(_ *http.Request) auth.Authenticatable { return nil }
func (g *recorderScheme) ID(_ *http.Request) interface{}            { return nil }
func (g *recorderScheme) Login(_ http.ResponseWriter, _ *http.Request, _ auth.Authenticatable, _ ...bool) error {
	return errors.New("recorderScheme.Login: should not be called in this test")
}
func (g *recorderScheme) LoginByID(_ http.ResponseWriter, _ *http.Request, _ interface{}, _ ...bool) error {
	return errors.New("recorderScheme.LoginByID: should not be called in this test")
}
func (g *recorderScheme) Attempt(_ http.ResponseWriter, _ *http.Request, _ map[string]interface{}, _ ...bool) (bool, error) {
	return false, nil
}
func (g *recorderScheme) Logout(_ http.ResponseWriter, _ *http.Request) error { return nil }
func (g *recorderScheme) SetUserStore(_ auth.UserStore)                       {}

// compile-time check: recorderScheme satisfies BOTH Scheme and the
// rotator-receiver capability the manager propagates to.
var (
	_ auth.Scheme                   = (*recorderScheme)(nil)
	_ auth.CSRFTokenRotatorReceiver = (*recorderScheme)(nil)
)

// csrfReplaceModule is a chain Module whose Boot replaces
// s.CSRF with a customised fake. Mirrors the velship.com pattern where
// a consumer bootstrapCSRF module swaps in its own CSRF instance.
type csrfReplaceModule struct {
	replacement *fakeCSRFRotator
}

func (p *csrfReplaceModule) Init(_ *app.Services) error { return nil }
func (p *csrfReplaceModule) Start(s *app.Services) error {
	// Capture the framework-built s.CSRF for the optional pre-fix
	// "did the framework wire the rotator at the wrong moment" probe;
	// the test does not need it but documents intent.
	s.CSRF = p.replacement
	return nil
}
func (p *csrfReplaceModule) Shutdown(_ context.Context) error { return nil }

// TestCSRFRotator_PointsToBootReplacement pins the boot-order fix that
// keeps the auth manager's CSRF token rotator aligned with the FINAL
// s.CSRF on Services (post chain module Boot) rather than the
// framework-built CSRF created during New().
//
// Pre-fix: app.go installed the rotator AT New() time, so any chain
// module that replaced s.CSRF in its Start() left the auth manager
// holding the original framework instance. RotateToken / RevokeToken /
// WriteXSRFCookie calls during Login / Logout / remember-cookie revival
// silently targeted a CSRF store no longer in the request path, and the
// first POST after login returned 419.
//
// Post-fix: bootstrap.go installs the rotator AFTER runModuleLifecycle
// returns, so the install picks up the consumer's replacement.
//
// Observation strategy: register a brand-new recorderScheme on the auth
// manager AFTER Bootstrap returns. Manager.RegisterScheme propagates the
// currently-installed csrfRotator to any scheme that implements the
// CSRFTokenRotatorReceiver capability (auth/auth.go line 319-322). Assert
// the recorderScheme received the fake replacement, NOT some other
// (framework-built) instance.
func TestCSRFRotator_PointsToBootReplacement(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	// Capture the framework-built CSRF so we can prove the post-Boot
	// instance differs and ensure the assertion is non-trivial.
	originalCSRF := a.CSRF
	if originalCSRF == nil {
		t.Fatal("framework-built a.CSRF is nil; cannot verify swap")
	}

	replacement := &fakeCSRFRotator{tag: "consumer-boot-replacement"}
	if any(replacement) == any(originalCSRF) {
		t.Fatal("test fake collides with framework-built instance")
	}

	a.Modules(func(r *chain.ModuleRegistry) {
		r.Add(&csrfReplaceModule{replacement: replacement})
	})

	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap() error: %v", err)
	}

	// Sanity: the module Boot actually swapped Services.CSRF.
	if a.Services.CSRF != replacement {
		t.Fatalf("Services.CSRF after Bootstrap: got %#v, want fake replacement %#v",
			a.Services.CSRF, replacement)
	}

	mgr, ok := a.Auth.(*auth.Manager)
	if !ok {
		t.Fatalf("a.Auth is not *auth.Manager: %T", a.Auth)
	}

	// Register a brand-new spy scheme. Manager.RegisterScheme propagates
	// the currently-installed csrfRotator to any scheme that implements
	// CSRFTokenRotatorReceiver. If bootstrap installed the rotator
	// AFTER the consumer's Boot swap, the spy receives the replacement.
	spy := &recorderScheme{}
	mgr.RegisterScheme("csrf-rotator-spy", spy)

	got := spy.lastRotator()
	if got == nil {
		t.Fatal("RegisterScheme did not propagate any rotator to the spy scheme; the boot-order install in bootstrap.go did not run")
	}
	if got != contract.CSRFTokenRotator(replacement) {
		t.Errorf("auth.Manager carries the WRONG CSRF token rotator after Bootstrap:\n  got      = %#v\n  want     = %#v (consumer Boot replacement)\n  original = %#v (framework-built, should NOT be installed)",
			got, replacement, originalCSRF)
	}
}

// TestCSRFRotator_WiredByNewWithoutBootstrap pins the direct-New
// audience: consumers that hold an *App returned by velocity.New() but
// never call Bootstrap()/Serve() (embed-mode apps, tests, scripts that
// drive auth/csrf operations directly). For this audience the
// framework-built s.CSRF must still be wired into auth.Manager at New()
// time so SessionScheme.Login / Logout / remember-cookie revival keep
// the CSRF token store aligned with the session lifecycle.
//
// Regression model: an earlier follow-up moved the install out of
// New() entirely into bootstrap(); the bootstrap-only install is
// correct for chain-module Boot swaps but silently broke this
// audience because their code path never ran bootstrap(). The current
// fix installs in BOTH places (New() and bootstrap()) and this test
// guards the New-only half.
//
// Observation strategy mirrors TestCSRFRotator_PointsToBootReplacement:
// register a spy scheme on the auth manager and check
// RegisterScheme propagated a non-nil rotator pointing at the
// framework-built s.CSRF (because no consumer Boot ran to swap it).
func TestCSRFRotator_WiredByNewWithoutBootstrap(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	// IMPORTANT: this test deliberately does NOT call a.Bootstrap().
	// It verifies that New() alone is enough to wire the rotator for
	// the direct-New audience.

	if a.CSRF == nil {
		t.Fatal("framework-built a.CSRF is nil; cannot verify wiring")
	}

	mgr, ok := a.Auth.(*auth.Manager)
	if !ok {
		t.Fatalf("a.Auth is not *auth.Manager: %T", a.Auth)
	}

	spy := &recorderScheme{}
	mgr.RegisterScheme("csrf-rotator-spy-new-only", spy)

	got := spy.lastRotator()
	if got == nil {
		t.Fatal("auth.Manager carries no CSRF token rotator after New() alone; direct-New consumers (no Bootstrap) lose session lifecycle rotation")
	}
	// The framework-built s.CSRF satisfies contract.CSRFTokenRotator.
	// Assert the spy received THAT instance, not some other.
	want, ok := a.CSRF.(contract.CSRFTokenRotator)
	if !ok {
		t.Fatalf("a.CSRF (%T) does not satisfy contract.CSRFTokenRotator; framework-built CSRF should always implement it", a.CSRF)
	}
	if got != want {
		t.Errorf("auth.Manager rotator mismatch:\n  got  = %#v\n  want = %#v (framework-built a.CSRF)", got, want)
	}
}
