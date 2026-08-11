package view

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/router"
)

// fakeSession records Flash/FlashMany interactions and Save invocations for
// assertion in tests. Only the methods exercised by the redirect helpers are
// non-trivial; the rest satisfy the auth.Session interface with stub behavior.
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

// The remaining auth.Session methods are unused by view.ReqEngine and return
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

var _ auth.Session = (*fakeSession)(nil)

// sessionAwareScheme is a Scheme that also satisfies auth.SessionAware so the
// ReqEngine flash path can locate the recording session.
type sessionAwareScheme struct {
	session auth.Session
}

func (g *sessionAwareScheme) Check(*http.Request) bool                { return false }
func (g *sessionAwareScheme) User(*http.Request) auth.Authenticatable { return nil }
func (g *sessionAwareScheme) ID(*http.Request) any                    { return nil }
func (g *sessionAwareScheme) SetUserStore(auth.UserStore)             {}
func (g *sessionAwareScheme) Logout(http.ResponseWriter, *http.Request) error {
	return nil
}
func (g *sessionAwareScheme) Login(http.ResponseWriter, *http.Request, auth.Authenticatable, ...bool) error {
	return nil
}
func (g *sessionAwareScheme) LoginByID(http.ResponseWriter, *http.Request, any, ...bool) error {
	return nil
}
func (g *sessionAwareScheme) Attempt(http.ResponseWriter, *http.Request, map[string]any, ...bool) (bool, error) {
	return false, nil
}
func (g *sessionAwareScheme) Session(*http.Request) auth.Session { return g.session }

var (
	_ auth.Scheme       = (*sessionAwareScheme)(nil)
	_ auth.SessionAware = (*sessionAwareScheme)(nil)
)

// nonSessionScheme satisfies auth.Scheme without implementing SessionAware so
// Manager.Session returns nil (e.g. JWT-only deployments).
type nonSessionScheme struct{}

func (g *nonSessionScheme) Check(*http.Request) bool                        { return false }
func (g *nonSessionScheme) User(*http.Request) auth.Authenticatable         { return nil }
func (g *nonSessionScheme) ID(*http.Request) any                            { return nil }
func (g *nonSessionScheme) SetUserStore(auth.UserStore)                     {}
func (g *nonSessionScheme) Logout(http.ResponseWriter, *http.Request) error { return nil }
func (g *nonSessionScheme) Login(http.ResponseWriter, *http.Request, auth.Authenticatable, ...bool) error {
	return nil
}
func (g *nonSessionScheme) LoginByID(http.ResponseWriter, *http.Request, any, ...bool) error {
	return nil
}
func (g *nonSessionScheme) Attempt(http.ResponseWriter, *http.Request, map[string]any, ...bool) (bool, error) {
	return false, nil
}

var _ auth.Scheme = (*nonSessionScheme)(nil)

// stubViewEngine implements contract.ViewEngine but is not a *view.Engine, so
// view.FromContext returns nil when this value is wired onto a context.
type stubViewEngine struct{}

func (stubViewEngine) Back(http.ResponseWriter, *http.Request) {}

var _ contract.ViewEngine = stubViewEngine{}

// stubAuthManager implements contract.AuthManager but is not an *auth.Manager,
// so auth.FromContext returns nil when this value is wired onto a context.
type stubAuthManager struct{}

func (stubAuthManager) Allows(*http.Request, string, ...any) bool { return false }
func (stubAuthManager) Authorize(*http.Request, string, ...any) error {
	return nil
}

var _ contract.AuthManager = stubAuthManager{}

// newRedirectCtx builds a router.Context wired with the given view engine and
// optional auth manager. Pass authMgr == nil to install a stub auth manager
// (one that auth.FromContext cannot recognise) so the call site does not
// panic on c.Auth().
func newRedirectCtx(t *testing.T, method, path string, engine contract.ViewEngine, authMgr contract.AuthManager) (*router.Context, *httptest.ResponseRecorder) {
	t.Helper()
	if engine == nil {
		t.Fatal("newRedirectCtx requires a non-nil view engine")
	}
	if authMgr == nil {
		authMgr = stubAuthManager{}
	}
	ctx, rec := router.NewTestContext(method, path)
	ctx.SetServices(&app.Services{View: engine, Auth: authMgr})
	return ctx, rec
}

func newAuthManagerWithSession(sess auth.Session) *auth.Manager {
	m := auth.NewManager()
	m.RegisterScheme("web", &sessionAwareScheme{session: sess})
	return m
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

func TestRedirect_TopLevel_NoEngineIsNoop(t *testing.T) {
	ctx, rec := router.NewTestContext("POST", "/submit")
	// View is wired to a stub that is not *view.Engine, so view.FromContext
	// returns nil and Redirect must be a no-op.
	ctx.SetServices(&app.Services{View: stubViewEngine{}, Auth: stubAuthManager{}})

	Redirect(ctx, "/foo")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want default 200 (no-op)", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Errorf("Location = %q, want empty (no-op)", got)
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

func TestFor_NoEngine_ReturnsNilAndChainIsNoop(t *testing.T) {
	ctx, rec := router.NewTestContext("POST", "/submit")
	ctx.SetServices(&app.Services{View: stubViewEngine{}, Auth: stubAuthManager{}})

	re := For(ctx)
	if re != nil {
		t.Fatalf("For with non-*Engine ViewEngine = %v, want nil", re)
	}

	// Every chain method must tolerate the nil receiver without panicking.
	re.Flash("k", "v").FlashMany(map[string]any{"a": 1}).Redirect("/foo")
	re.Location("/bar")
	re.Back()
	if err := re.Render("Comp"); err != nil {
		t.Errorf("Render on nil ReqEngine = %v, want nil", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want default 200", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Errorf("Location = %q, want empty", got)
	}
}

func TestFor_FlashThenRedirect_FlashesAndSaves(t *testing.T) {
	engine := newTestEngine(t)
	sess := &fakeSession{}
	ctx, rec := newRedirectCtx(t, "POST", "/submit", engine, newAuthManagerWithSession(sess))

	For(ctx).Flash("error", "x").Redirect("/foo")

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
	if got := sess.saves(); got != 1 {
		t.Errorf("Save call count = %d, want 1", got)
	}
}

func TestFor_TwoFlashThenRedirect_AppliesBothAndSavesOnce(t *testing.T) {
	engine := newTestEngine(t)
	sess := &fakeSession{}
	ctx, _ := newRedirectCtx(t, "POST", "/submit", engine, newAuthManagerWithSession(sess))

	For(ctx).Flash("error", "x").Flash("info", "y").Redirect("/foo")

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
	if got := sess.saves(); got != 1 {
		t.Errorf("Save call count = %d, want 1", got)
	}
}

func TestFor_RedirectWithoutFlash_DoesNotLoadOrSaveSession(t *testing.T) {
	engine := newTestEngine(t)
	sess := &fakeSession{}
	ctx, rec := newRedirectCtx(t, "POST", "/submit", engine, newAuthManagerWithSession(sess))

	For(ctx).Redirect("/foo")

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
	if got := sess.saves(); got != 0 {
		t.Errorf("Save call count = %d, want 0 (flashed=false short-circuit)", got)
	}
	if got := sess.calls(); len(got) != 0 {
		t.Errorf("Flash calls = %#v, want none", got)
	}
}

func TestFor_FlashMany_AppliesAllAndSavesOnce(t *testing.T) {
	engine := newTestEngine(t)
	sess := &fakeSession{}
	ctx, _ := newRedirectCtx(t, "POST", "/submit", engine, newAuthManagerWithSession(sess))

	For(ctx).FlashMany(map[string]any{
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
	if n := sess.saves(); n != 1 {
		t.Errorf("Save call count = %d, want 1", n)
	}
}

func TestFor_RenderWithPriorFlash_SavesAndRenders(t *testing.T) {
	engine := newTestEngine(t)
	sess := &fakeSession{}
	ctx, rec := newRedirectCtx(t, "GET", "/page", engine, newAuthManagerWithSession(sess))
	ctx.Request.Header.Set("X-Inertia", "true")

	err := For(ctx).Flash("toast", "saved").Render("Comp")
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	if n := sess.saves(); n != 1 {
		t.Errorf("Save call count = %d, want 1", n)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 from Inertia JSON render", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got == "" {
		t.Errorf("Content-Type empty; expected the engine to write a JSON response")
	}
}

// ---- Auth wiring edge cases --------------------------------------------

func TestFor_Flash_NoAuthManager_IsNoopAndDoesNotSave(t *testing.T) {
	engine := newTestEngine(t)
	// stubAuthManager wired so c.Auth() does not panic but
	// auth.FromContext returns nil (it is not an *auth.Manager).
	ctx, rec := newRedirectCtx(t, "POST", "/submit", engine, stubAuthManager{})

	re := For(ctx)
	if re == nil {
		t.Fatal("For returned nil; expected a live ReqEngine when *view.Engine is wired")
	}

	// Flash must not panic and must leave flashed=false so the subsequent
	// Redirect skips session Save entirely.
	re.Flash("error", "x").Redirect("/foo")

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/foo" {
		t.Errorf("Location = %q, want /foo", got)
	}
	// Nothing observable on the session: there is no real session here.
	// What we assert is that the flow completed without panic and that
	// the response carries the expected redirect.
}

func TestFor_Flash_SchemeNotSessionAware_IsNoop(t *testing.T) {
	engine := newTestEngine(t)
	mgr := auth.NewManager()
	mgr.RegisterScheme("web", &nonSessionScheme{})

	ctx, rec := newRedirectCtx(t, "POST", "/submit", engine, mgr)
	sess := &fakeSession{} // unused; nonSessionScheme never hands it out
	_ = sess

	For(ctx).Flash("error", "x").Redirect("/foo")

	// Manager.Session returns nil because nonSessionScheme does not
	// implement SessionAware, so the ReqEngine never acquires a session.
	// The redirect still fires.
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/foo" {
		t.Errorf("Location = %q, want /foo", got)
	}
}
