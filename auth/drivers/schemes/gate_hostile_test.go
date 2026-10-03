package schemes

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/router"
)

// gateHookUsers is a revokeTestStore that runs hook, when set, before a
// user lookup and before a remember-token swap.
type gateHookUsers struct {
	*revokeTestStore
	onFind, onSwap atomic.Pointer[func()]
}

func (u *gateHookUsers) FindByIDCtx(ctx context.Context, id interface{}) (contract.Authenticatable, error) {
	if p := u.onFind.Load(); p != nil {
		(*p)()
	}
	return u.revokeTestStore.FindByIDCtx(ctx, id)
}

func (u *gateHookUsers) CompareAndSwapRememberToken(ctx context.Context, user contract.Authenticatable, oldToken, newToken string) (bool, error) {
	if p := u.onSwap.Load(); p != nil {
		(*p)()
	}
	return u.revokeTestStore.CompareAndSwapRememberToken(ctx, user, oldToken, newToken)
}

// gateHookRotator is a CSRF token rotator that runs hook, when set, as it
// rotates a token.
type gateHookRotator struct {
	onRotate atomic.Pointer[func()]
}

func (r *gateHookRotator) RotateToken(context.Context, string, string) error {
	if p := r.onRotate.Load(); p != nil {
		(*p)()
	}
	return nil
}
func (*gateHookRotator) RevokeToken(context.Context, string) error                    { return nil }
func (*gateHookRotator) WriteXSRFCookie(context.Context, http.ResponseWriter, string) {}
func (*gateHookRotator) ClearXSRFCookie(http.ResponseWriter, *http.Request)           {}

// The session scheme runs the user code of an authentication operation
// (a user lookup in Logout, the remember-token swap of a recall, the CSRF
// rotation in Login, the session save of the commit) with no lock held,
// holding only the request's non-blocking gate. User code that calls back
// into the scheme for the same request gets auth.ErrOperationInProgress
// at once and the operation completes; while the user code blocks, another
// goroutine of the request is refused at once instead of waiting; and a
// panic in it leaves the scheme serving the next request.
func TestSessionScheme_OperationUserCodeRunsWithTheGateOnly(t *testing.T) {
	type rig struct {
		scheme  *SessionScheme
		users   *gateHookUsers
		rotator *gateHookRotator
		b       *rememberBrowser
		req     atomic.Pointer[http.Request]
	}
	entries := []struct {
		name string
		// arm installs hook at the user-code site; prepare readies the
		// browser; method and path are the request that reaches it.
		arm          func(rg *rig, hook func())
		prepare      func(rg *rig)
		method, path string
		// read marks a read of the user: another goroutine's read waits
		// for it instead of failing closed.
		read bool
	}{
		{"Logout user lookup", func(rg *rig, hook func()) { rg.users.onFind.Store(&hook) }, func(rg *rig) {
			rg.b.do(http.MethodPost, "/login")
		}, http.MethodPost, "/logout", false},
		{"recall remember-token swap", func(rg *rig, hook func()) { rg.users.onSwap.Store(&hook) }, func(rg *rig) {
			rg.b.do(http.MethodPost, "/login")
			rg.b.replayRememberOnly(*rg.b.cookies[rememberCookieName])
		}, http.MethodGet, "/read", true},
		{"read user lookup", func(rg *rig, hook func()) { rg.users.onFind.Store(&hook) }, func(rg *rig) {
			rg.b.do(http.MethodPost, "/login")
		}, http.MethodGet, "/read", true},
		{"Login CSRF rotation", func(rg *rig, hook func()) { rg.rotator.onRotate.Store(&hook) }, func(*rig) {}, http.MethodPost, "/login", false},
		{"commit session save", func(rg *rig, hook func()) {
			orig := saveSessionFromMiddleware
			saveSessionFromMiddleware = func(g *SessionScheme, w http.ResponseWriter, s contract.Session) error {
				hook()
				return orig(g, w, s)
			}
			t.Cleanup(func() { saveSessionFromMiddleware = orig })
		}, func(*rig) {}, http.MethodGet, "/touch", false},
	}
	for _, mode := range hostile.Modes() {
		for _, e := range entries {
			t.Run(mode.String()+"/"+e.name, func(t *testing.T) {
				installLifetimeClock(t)
				scheme, _ := newLifetimeSchemeFor(t, 120, 0, lifetimeModes[0])
				rg := &rig{scheme: scheme, rotator: &gateHookRotator{}}
				rg.users = &gateHookUsers{revokeTestStore: userStoreOf(t, scheme)}
				scheme.SetUserStore(rg.users)
				scheme.SetCSRFTokenRotator(rg.rotator)

				rg.b = newRememberBrowser(t, scheme)
				r := router.New()
				r.Use(scheme.SessionMiddleware())
				keep := func(c *router.Context) { rg.req.Store(c.Request) }
				r.Post("/login", func(c *router.Context) error {
					keep(c)
					if err := scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}, true); err != nil {
						return err
					}
					return c.String(http.StatusOK, "in")
				})
				r.Post("/logout", func(c *router.Context) error {
					keep(c)
					if err := scheme.Logout(c.Response, c.Request); err != nil {
						return err
					}
					return c.String(http.StatusOK, "out")
				})
				r.Get("/read", func(c *router.Context) error {
					keep(c)
					_ = scheme.User(c.Request)
					return c.String(http.StatusOK, "read")
				})
				r.Get("/touch", func(c *router.Context) error {
					keep(c)
					scheme.Session(c.Request).Put("touched", true)
					return c.String(http.StatusOK, "touched")
				})
				r.Get("/check", func(c *router.Context) error {
					ok, err := scheme.CheckWithError(c.Request)
					rg.b.lastErr = err
					if !ok {
						return c.String(http.StatusUnauthorized, "out")
					}
					return c.String(http.StatusOK, "in")
				})
				rg.b.handler = r
				e.prepare(rg)

				var reentered atomic.Pointer[error]
				code := hostile.New(t, mode, func() {
					_, err := scheme.CheckWithError(rg.req.Load())
					reentered.Store(&err)
				})
				e.arm(rg, code.Run)

				switch mode {
				case hostile.Block:
					done := make(chan struct{})
					go func() { //safe-goroutine: the test releases the block below and waits for it
						defer close(done)
						rg.b.do(e.method, e.path)
					}()
					<-code.Entered()
					if e.read {
						// Another goroutine's read waits for the blocked
						// read and then returns what it resolved.
						req := rg.req.Load()
						sibling := make(chan error, 1)
						go func() { //safe-goroutine: the test releases the block below and waits for it
							_, err := scheme.CheckWithError(req)
							sibling <- err
						}()
						h := req.Context().Value(sessionCtxKey{}).(*sessionHolder)
						hostile.Within(t, hostile.Deadline, func() {
							for {
								h.mu.RLock()
								n := h.waiters
								h.mu.RUnlock()
								if n == 1 {
									return
								}
								runtime.Gosched()
							}
						})
						code.Release()
						hostile.Within(t, hostile.Deadline, func() { <-done })
						hostile.Within(t, hostile.Deadline, func() {
							if err := <-sibling; errors.Is(err, auth.ErrOperationInProgress) {
								t.Errorf("a read that waited for the blocked read = %v, want its outcome", err)
							}
						})
						break
					}
					hostile.Within(t, hostile.Deadline, func() {
						if _, err := scheme.CheckWithError(rg.req.Load()); !errors.Is(err, auth.ErrOperationInProgress) {
							t.Errorf("a read of the request while its operation blocks = %v, want auth.ErrOperationInProgress", err)
						}
					})
					code.Release()
					hostile.Within(t, hostile.Deadline, func() { <-done })
				case hostile.Reenter:
					hostile.Within(t, hostile.Deadline, func() { rg.b.do(e.method, e.path) })
					var got error
					if p := reentered.Load(); p != nil {
						got = *p
					}
					if !errors.Is(got, auth.ErrOperationInProgress) {
						t.Errorf("a call back into the scheme from its user code = %v, want auth.ErrOperationInProgress", got)
					}
				case hostile.Panic:
					hostile.Within(t, hostile.Deadline, func() { rg.b.do(e.method, e.path) })
				}
				code.Disarm()
				if code.Calls() == 0 {
					t.Fatal("premise: the user code was not reached")
				}

				// The scheme serves the next requests: a sign-in works and
				// is seen on the one after it.
				hostile.Within(t, hostile.Deadline, func() {
					rg.b.do(http.MethodPost, "/login")
					if !rg.b.signedIn() {
						t.Errorf("not signed in after a fresh login (err=%v)", rg.b.lastErr)
					}
				})
			})
		}
	}
}

// A response committed while a Login of the same request is in flight
// saves nothing and returns at once, and the Login is then refused: the
// client is not signed in, and no remember cookie is delivered.
func TestSessionScheme_CommitDuringLoginSavesNothing(t *testing.T) {
	installLifetimeClock(t)
	scheme, _ := newLifetimeSchemeFor(t, 120, 0, lifetimeModes[0])
	rotator := &gateHookRotator{}
	scheme.SetCSRFTokenRotator(rotator)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	hook := func() {
		close(entered)
		<-release
	}
	rotator.onRotate.Store(&hook)

	b := newRememberBrowser(t, scheme)
	var loginErr atomic.Pointer[error]
	r := router.New()
	r.Use(scheme.SessionMiddleware())
	r.Post("/race", func(c *router.Context) error {
		loginDone := make(chan struct{})
		go func() {
			defer close(loginDone)
			err := scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}, true)
			loginErr.Store(&err)
		}()
		<-entered
		hostile.Within(t, hostile.Deadline, func() { _ = c.String(http.StatusOK, "done") })
		close(release)
		<-loginDone
		return nil
	})
	r.Get("/check", func(c *router.Context) error {
		if ok, _ := scheme.CheckWithError(c.Request); !ok {
			return c.String(http.StatusUnauthorized, "out")
		}
		return c.String(http.StatusOK, "in")
	})
	b.handler = r

	hostile.Within(t, 2*hostile.Deadline, func() { b.do(http.MethodPost, "/race") })
	if p := loginErr.Load(); p == nil || *p == nil {
		t.Fatal("Login whose response was committed meanwhile returned nil; want it refused")
	}
	if b.liveCookie(rememberCookieName) != nil {
		t.Error("a remember cookie was delivered for a Login the commit did not save")
	}
	if b.signedIn() {
		t.Error("the client is signed in although the commit ran while its Login was in flight")
	}
}

// parkingRecords is a server session store whose first Delete blocks
// until released, so a test can act while Logout's teardown is parked.
type parkingRecords struct {
	auth.ServerSessionStore
	parked  atomic.Bool
	entered chan struct{}
	release chan struct{}
	deleted atomic.Pointer[string]
}

func (p *parkingRecords) Delete(ctx context.Context, id string) error {
	if p.parked.CompareAndSwap(false, true) {
		p.deleted.Store(&id)
		close(p.entered)
		<-p.release
	}
	return p.ServerSessionStore.Delete(ctx, id)
}

// Logout holds the request's reservation through its server-side
// teardown: a Login of the same request while that teardown is parked is
// refused with auth.ErrOperationInProgress and changes nothing (no new
// session id, no record, the stored remember token as it was). Once the
// Logout returns, a Login of the request signs in on a fresh session. The
// old session's record is gone and neither old credential signs in.
func TestSessionScheme_LogoutTeardownRefusesARacingLogin(t *testing.T) {
	installLifetimeClock(t)
	scheme, mem := newLifetimeSchemeFor(t, 120, 0, lifetimeModes[0])
	users := userStoreOf(t, scheme)
	records := &parkingRecords{ServerSessionStore: mem, entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-records.release:
		default:
			close(records.release)
		}
	})

	b := newRememberBrowser(t, scheme)
	b.do(http.MethodPost, "/login")
	oldRemember := *b.cookies[rememberCookieName]
	oldSession := *b.cookies[sessionCookieName]
	scheme.SetServerSessionStore(records)

	var (
		oldID, parkedID, newID  string
		loggedOutID             string
		tokenBefore, tokenAfter string
		logoutErr, racingErr    error
		loginErr                error
	)
	r := router.New()
	r.Use(scheme.SessionMiddleware())
	r.Post("/switch", func(c *router.Context) error {
		oldID = scheme.Session(c.Request).ID()
		tokenBefore = users.token("u1")
		logoutDone := make(chan struct{})
		go func() {
			defer close(logoutDone)
			logoutErr = scheme.Logout(c.Response, c.Request)
		}()
		hostile.Within(t, hostile.Deadline, func() { <-records.entered })
		hostile.Within(t, hostile.Deadline, func() {
			racingErr = scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}, true)
		})
		parkedID = scheme.Session(c.Request).ID()
		tokenAfter = users.token("u1")
		close(records.release)
		hostile.Within(t, hostile.Deadline, func() { <-logoutDone })
		loggedOutID = scheme.Session(c.Request).ID()
		loginErr = scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}, true)
		newID = scheme.Session(c.Request).ID()
		return c.String(http.StatusOK, "switched")
	})
	r.Get("/check", func(c *router.Context) error {
		if ok, _ := scheme.CheckWithError(c.Request); !ok {
			return c.String(http.StatusUnauthorized, "out")
		}
		return c.String(http.StatusOK, "in")
	})
	b.handler = r
	b.do(http.MethodPost, "/switch")

	if logoutErr != nil {
		t.Fatalf("Logout = %v, want nil", logoutErr)
	}
	if !errors.Is(racingErr, auth.ErrOperationInProgress) {
		t.Fatalf("Login while the logout's teardown was parked = %v, want auth.ErrOperationInProgress", racingErr)
	}
	if p := records.deleted.Load(); p == nil || *p != oldID {
		t.Fatalf("the parked delete was %v, want the old session id %q", p, oldID)
	}
	if parkedID != loggedOutID {
		t.Errorf("the session id moved while the refused Login ran: %q while parked, %q once the logout ended", parkedID, loggedOutID)
	}
	if tokenAfter != "" || tokenBefore == "" {
		t.Errorf("premise or refused Login: remember token %q before the logout, %q while its teardown was parked; want set, then cleared by the logout alone", tokenBefore, tokenAfter)
	}
	if loginErr != nil {
		t.Fatalf("Login after the Logout returned = %v, want nil", loginErr)
	}
	if newID == "" || newID == oldID {
		t.Fatalf("Login after the Logout did not move the session to a new id (old %q, new %q)", oldID, newID)
	}
	if _, err := mem.Get(context.Background(), newID); err != nil {
		t.Errorf("the record Login wrote for its new id is missing: %v", err)
	}
	if _, err := mem.Get(context.Background(), oldID); err == nil {
		t.Error("the old session's record survived the logout")
	}

	// Neither old credential signs in any more.
	b.cookies = map[string]*http.Cookie{rememberCookieName: &oldRemember}
	if b.signedIn() {
		t.Error("the old remember cookie still signs in after the logout")
	}
	b.cookies = map[string]*http.Cookie{sessionCookieName: &oldSession}
	if b.signedIn() {
		t.Error("the old session cookie still signs in after the logout")
	}
}
