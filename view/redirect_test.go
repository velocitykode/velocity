package view

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/router"
)

// fakeSession records Flash/FlashMany interactions and Save invocations for
// assertion in tests. Only the methods exercised by the redirect helpers are
// non-trivial; the rest satisfy the contract.Session interface with stub behavior.
type fakeSession struct {
	mu         sync.Mutex
	flashCalls []flashEntry
	saveCalled int
	saveErr    error
}

type flashEntry struct {
	key   string
	value any
}

func (s *fakeSession) Flash(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flashCalls = append(s.flashCalls, flashEntry{key: key, value: value})
}

func (s *fakeSession) Save(http.ResponseWriter) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveCalled++
	return s.saveErr
}

func (s *fakeSession) calls() []flashEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]flashEntry, len(s.flashCalls))
	copy(out, s.flashCalls)
	return out
}

func (s *fakeSession) saves() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveCalled
}

// The remaining contract.Session methods are unused by view.ReqEngine and return
// zero values.
func (s *fakeSession) ID() string                 { return "fake-session" }
func (s *fakeSession) Get(string) any             { return nil }
func (s *fakeSession) Put(string, any)            {}
func (s *fakeSession) Has(string) bool            { return false }
func (s *fakeSession) Remove(string)              {}
func (s *fakeSession) Clear()                     {}
func (s *fakeSession) Regenerate() error          { return nil }
func (s *fakeSession) Invalidate() error          { return nil }
func (s *fakeSession) GetFlash(string) any        { return nil }
func (s *fakeSession) FlushFlash() map[string]any { return nil }

var _ contract.Session = (*fakeSession)(nil)

// foreignEngine implements contract.ViewEngine but is not a *view.Engine:
// a module's own engine. It records the calls it receives.
type foreignEngine struct {
	calls []string
}

func (e *foreignEngine) Back(http.ResponseWriter, *http.Request) {
	e.calls = append(e.calls, "Back")
}

func (e *foreignEngine) Redirect(_ http.ResponseWriter, _ *http.Request, url string) {
	e.calls = append(e.calls, "Redirect "+url)
}

func (e *foreignEngine) LocationExternal(_ http.ResponseWriter, _ *http.Request, url string) {
	e.calls = append(e.calls, "LocationExternal "+url)
}

var _ contract.ViewEngine = (*foreignEngine)(nil)

// stubAuthManager implements contract.AuthManager but is not an *auth.Manager.
type stubAuthManager struct {
	// contract.AuthManager supplies the methods this fake does not use.
	contract.AuthManager
}

func (stubAuthManager) Allows(*http.Request, string, ...any) bool { return false }
func (stubAuthManager) Authorize(*http.Request, string, ...any) error {
	return nil
}

var _ contract.AuthManager = stubAuthManager{}

// newRedirectCtx builds a router.Context wired with the given view engine
// and a Services.FlashBag that hands out bag (nil: the request carries no
// session). A stub auth manager is installed so c.Auth() does not panic.
func newRedirectCtx(t *testing.T, method, path string, engine contract.ViewEngine, bag contract.FlashBag) (*router.Context, *httptest.ResponseRecorder) {
	t.Helper()
	if engine == nil {
		t.Fatal("newRedirectCtx requires a non-nil view engine")
	}
	ctx, rec := router.NewTestContext(method, path)
	ctx.SetServices(&app.Services{
		View: engine,
		Auth: stubAuthManager{},
		FlashBag: func(*http.Request) contract.FlashBag {
			if bag == nil {
				return nil
			}
			return bag
		},
	})
	return ctx, rec
}

// ---- Top-level sugar ----------------------------------------------------

func TestRedirect_TopLevel_WithEngine(t *testing.T) {
	engine := newTestEngine(t)
	ctx, rec := newRedirectCtx(t, "POST", "/submit", engine, nil)

	Redirect(ctx, "/foo")

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/foo" {
		t.Errorf("Location = %q, want /foo", got)
	}
}

func TestRedirect_TopLevel_InertiaRequestSetsInertiaLocation(t *testing.T) {
	engine := newTestEngine(t)
	ctx, rec := newRedirectCtx(t, "GET", "/page", engine, nil)
	ctx.Request.Header.Set("X-Inertia", "true")

	// Use Location rather than Redirect so the Inertia-specific 409 +
	// X-Inertia-Location pair is the observable signal.
	Location(ctx, "/foo")

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	if got := rec.Header().Get("X-Inertia-Location"); got != "/foo" {
		t.Errorf("X-Inertia-Location = %q, want /foo", got)
	}
}

// A view engine that is not a *view.Engine is present: the helpers whose
// method the contract carries call it.
func TestHelpers_ForeignEngine_ContractMethodsReachIt(t *testing.T) {
	tests := []struct {
		name string
		call func(*router.Context) error
		want string
	}{
		{"Redirect", func(c *router.Context) error { return Redirect(c, "/foo") }, "Redirect /foo"},
		{"LocationExternal", func(c *router.Context) error { return LocationExternal(c, "https://x.example") }, "LocationExternal https://x.example"},
		{"Back", func(c *router.Context) error { return Back(c) }, "Back"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := &foreignEngine{}
			ctx, _ := router.NewTestContext("POST", "/submit")
			ctx.SetServices(&app.Services{View: engine, Auth: stubAuthManager{}})
			if err := tt.call(ctx); err != nil {
				t.Fatalf("%s = %v, want nil", tt.name, err)
			}
			if len(engine.calls) != 1 || engine.calls[0] != tt.want {
				t.Fatalf("the engine received %v, want [%s]", engine.calls, tt.want)
			}
		})
	}
}

// The helpers that need the framework's engine report a foreign one with
// an error naming its type, not with the absence error: the service is
// configured. They write nothing and call nothing on it.
func TestHelpers_ForeignEngine_EngineOnlyHelpersNameItsType(t *testing.T) {
	tests := []struct {
		name string
		call func(*router.Context) error
	}{
		{"Location", func(c *router.Context) error { return Location(c, "/foo") }},
		{"Render", func(c *router.Context) error { return Render(c, "Comp") }},
		{"For", func(c *router.Context) error {
			re, err := For(c)
			if re != nil {
				t.Errorf("For = %v, want nil", re)
			}
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := &foreignEngine{}
			ctx, rec := router.NewTestContext("POST", "/submit")
			ctx.SetServices(&app.Services{View: engine, Auth: stubAuthManager{}})
			err := tt.call(ctx)
			if err == nil {
				t.Fatalf("%s = nil, want an error naming the engine's type", tt.name)
			}
			if errors.Is(err, contract.ErrServiceNotConfigured) {
				t.Fatalf("%s = %v, which reports a configured service as absent", tt.name, err)
			}
			if !strings.Contains(err.Error(), "*view.foreignEngine") {
				t.Fatalf("%s = %v, want the engine's type named", tt.name, err)
			}
			if len(engine.calls) != 0 {
				t.Errorf("the engine received %v, want nothing", engine.calls)
			}
			if rec.Code != http.StatusOK || rec.Body.Len() != 0 || len(rec.Header()) != 0 {
				t.Errorf("wrote status %d body %q headers %v, want nothing", rec.Code, rec.Body.String(), rec.Header())
			}
		})
	}
}

func TestLocation_TopLevel_NonInertiaIs302(t *testing.T) {
	engine := newTestEngine(t)
	ctx, rec := newRedirectCtx(t, "GET", "/page", engine, nil)

	Location(ctx, "/foo")

	if rec.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/foo" {
		t.Errorf("Location = %q, want /foo", got)
	}
}

func TestBack_TopLevel_UsesReferer(t *testing.T) {
	engine := newTestEngine(t)
	ctx, rec := newRedirectCtx(t, "POST", "/submit", engine, nil)
	ctx.Request.Header.Set("Referer", "/previous")

	Back(ctx)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/previous" {
		t.Errorf("Location = %q, want /previous", got)
	}
}

// ---- For chain ----------------------------------------------------------

func TestFor_NoEngine_Reports(t *testing.T) {
	ctx, rec := router.NewTestContext("POST", "/submit")
	ctx.SetServices(&app.Services{Auth: stubAuthManager{}})

	re, err := For(ctx)
	var snc *contract.ServiceNotConfiguredError
	if re != nil || !errors.As(err, &snc) || snc.Service != "view" {
		t.Fatalf("For without a view engine = %v, %v; want nil and a ServiceNotConfiguredError naming view", re, err)
	}
	if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" {
		t.Errorf("For wrote status %d Location %q, want nothing written", rec.Code, rec.Header().Get("Location"))
	}
}

// mustFor is For for a context known to carry an engine.
func mustFor(t *testing.T, ctx *router.Context) *ReqEngine {
	t.Helper()
	re, err := For(ctx)
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	return re
}

func TestFor_FlashThenRedirect_FlashesWithoutSaving(t *testing.T) {
	engine := newTestEngine(t)
	sess := &fakeSession{}
	ctx, rec := newRedirectCtx(t, "POST", "/submit", engine, sess)

	mustFor(t, ctx).Flash("error", "x").Redirect("/foo")

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/foo" {
		t.Errorf("Location = %q, want /foo", got)
	}
	calls := sess.calls()
	if len(calls) != 1 || calls[0].key != "error" || calls[0].value != "x" {
		t.Errorf("Flash calls = %#v, want one call (error, x)", calls)
	}
	if got := sess.saves(); got != 0 {
		t.Errorf("Save call count = %d, want 0 (the session middleware saves)", got)
	}
}

func TestFor_TwoFlashThenRedirect_AppliesBothWithoutSaving(t *testing.T) {
	engine := newTestEngine(t)
	sess := &fakeSession{}
	ctx, _ := newRedirectCtx(t, "POST", "/submit", engine, sess)

	mustFor(t, ctx).Flash("error", "x").Flash("info", "y").Redirect("/foo")

	calls := sess.calls()
	if len(calls) != 2 {
		t.Fatalf("Flash calls = %d, want 2", len(calls))
	}
	if calls[0].key != "error" || calls[0].value != "x" {
		t.Errorf("first flash = %#v, want (error,x)", calls[0])
	}
	if calls[1].key != "info" || calls[1].value != "y" {
		t.Errorf("second flash = %#v, want (info,y)", calls[1])
	}
	if got := sess.saves(); got != 0 {
		t.Errorf("Save call count = %d, want 0 (the session middleware saves)", got)
	}
}

func TestFor_RedirectWithoutFlash_DoesNotLoadOrSaveSession(t *testing.T) {
	engine := newTestEngine(t)
	sess := &fakeSession{}
	ctx, rec := newRedirectCtx(t, "POST", "/submit", engine, sess)

	mustFor(t, ctx).Redirect("/foo")

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
	if got := sess.saves(); got != 0 {
		t.Errorf("Save call count = %d, want 0", got)
	}
	if got := sess.calls(); len(got) != 0 {
		t.Errorf("Flash calls = %#v, want none", got)
	}
}

func TestFor_FlashMany_AppliesAllWithoutSaving(t *testing.T) {
	engine := newTestEngine(t)
	sess := &fakeSession{}
	ctx, _ := newRedirectCtx(t, "POST", "/submit", engine, sess)

	mustFor(t, ctx).FlashMany(map[string]any{
		"success": "a",
		"info":    "b",
	}).Redirect("/foo")

	calls := sess.calls()
	if len(calls) != 2 {
		t.Fatalf("Flash calls = %d, want 2", len(calls))
	}
	// Map iteration order is unspecified; collect into a lookup map.
	got := map[string]any{}
	for _, c := range calls {
		got[c.key] = c.value
	}
	if got["success"] != "a" || got["info"] != "b" {
		t.Errorf("Flash calls = %#v, want success=a + info=b", got)
	}
	if n := sess.saves(); n != 0 {
		t.Errorf("Save call count = %d, want 0 (the session middleware saves)", n)
	}
}

func TestFor_RenderWithPriorFlash_RendersWithoutSaving(t *testing.T) {
	engine := newTestEngine(t)
	sess := &fakeSession{}
	ctx, rec := newRedirectCtx(t, "GET", "/page", engine, sess)
	ctx.Request.Header.Set("X-Inertia", "true")

	err := mustFor(t, ctx).Flash("toast", "saved").Render("Comp")
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	if n := sess.saves(); n != 0 {
		t.Errorf("Save call count = %d, want 0 (the session middleware saves)", n)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 from Inertia JSON render", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got == "" {
		t.Errorf("Content-Type empty; expected the engine to write a JSON response")
	}
}

// ---- Session edge cases -------------------------------------------------

func TestFor_Flash_NoSession_IsNoop(t *testing.T) {
	engine := newTestEngine(t)
	// The request carries no session: Services.FlashBag returns nil, as it
	// does when the default scheme keeps no session (JWT-only).
	ctx, rec := newRedirectCtx(t, "POST", "/submit", engine, nil)

	re := mustFor(t, ctx)
	re.Flash("error", "x").Redirect("/foo")

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/foo" {
		t.Errorf("Location = %q, want /foo", got)
	}
}

func TestFor_Flash_NoServices_IsNoop(t *testing.T) {
	engine := newTestEngine(t)
	ctx, rec := newRedirectCtx(t, "POST", "/submit", engine, nil)
	ctx.ServicesIfSet().FlashBag = nil

	mustFor(t, ctx).Flash("error", "x").Redirect("/foo")

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
}
