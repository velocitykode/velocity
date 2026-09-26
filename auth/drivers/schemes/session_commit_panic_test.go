package schemes

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/router"
)

// panicRememberStore is a user store whose remember-token write panics
// while armed, the way an application's user store can fail when the
// commit settles a remember-me sign-in.
type panicRememberStore struct {
	*revokeTestStore
	armed *atomic.Bool
}

func (p *panicRememberStore) UpdateRememberTokenCtx(ctx context.Context, user auth.Authenticatable, token string) error {
	if p.armed.Load() {
		panic("user store failed to store the remember token")
	}
	return p.revokeTestStore.UpdateRememberTokenCtx(ctx, user, token)
}

// commitPanicRig is one application of two instances sharing a server
// session store, whose user store and session save can be made to panic.
// An armed save writes a deletion of the session cookie and then panics:
// a save that fails part way through.
type commitPanicRig struct {
	instance      func() *SessionScheme
	rememberPanic atomic.Bool
	savePanic     atomic.Bool
}

func newCommitPanicRig(t *testing.T, serverSide bool) *commitPanicRig {
	t.Helper()
	rig := &commitPanicRig{}
	cfg := teardownConfig()
	enc := teardownEncryptor(t)
	shared := session.NewMemoryStore()
	users := &panicRememberStore{
		revokeTestStore: &revokeTestStore{users: map[string]*revokeTestUser{"u1": {id: "u1"}}},
		armed:           &rig.rememberPanic,
	}
	orig := saveSessionFromMiddleware
	saveSessionFromMiddleware = func(g *SessionScheme, w http.ResponseWriter, s auth.Session) error {
		if rig.savePanic.Load() {
			http.SetCookie(w, &http.Cookie{Name: "vel_session", Value: "", Path: "/", MaxAge: -1})
			panic("session store failed while saving")
		}
		return orig(g, w, s)
	}
	t.Cleanup(func() { saveSessionFromMiddleware = orig })
	rig.instance = func() *SessionScheme {
		var opts []SessionSchemeOption
		if serverSide {
			store, err := session.NewServerStore(cfg, shared)
			if err != nil {
				t.Fatalf("NewServerStore: %v", err)
			}
			opts = append(opts, WithSessionStore(store))
		}
		scheme, err := NewSessionScheme(users, cfg, enc, opts...)
		if err != nil {
			t.Fatalf("NewSessionScheme: %v", err)
		}
		scheme.SetServerSessionStore(shared)
		return scheme
	}
	return rig
}

// A panic inside the commit, before any queued write is delivered, leaves
// no lock held and the queue closed. Covered for a remember-me sign-in
// whose user store panics when the commit settles the credential after the
// save, for a settlement that panics after a deletion of the session
// cookie was staged, and for a session store whose Save itself panics. The
// router recovers the panic and its error handler reads the signed-in user
// (it would wait forever on a lifecycle lock the panic left held); a write
// queued after the recovery is refused. A settlement panic follows a
// successful save, so a staged deletion ends the session the save issued;
// a save that panicked issued nothing, so its deletion ends nothing.
func TestSessionMiddleware_PanicInsideTheCommitReleasesTheLifecycleLock(t *testing.T) {
	type outcome int
	const (
		signedOutFirst outcome = iota
		sessionEnded
		sessionKept
	)
	cases := []struct {
		name  string
		start outcome
		// handle is the route's handler; it arms the failure it needs.
		handle func(rig *commitPanicRig, scheme *SessionScheme, c *router.Context) error
		want   outcome
	}{
		{"remember settlement panics after the save", signedOutFirst, func(rig *commitPanicRig, scheme *SessionScheme, c *router.Context) error {
			rig.rememberPanic.Store(true)
			if err := scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}, true); err != nil {
				return err
			}
			return c.String(http.StatusOK, "in")
		}, signedOutFirst},
		{"settlement panics after a staged deletion", sessionKept, func(rig *commitPanicRig, scheme *SessionScheme, c *router.Context) error {
			holder := c.Request.Context().Value(sessionCtxKey{}).(*sessionHolder)
			header := c.Response.Header()
			holder.lifecycle.Lock()
			holder.queueCredentialWrite(afterSaveWrite{
				settle: func() {
					header.Add("Set-Cookie", (&http.Cookie{Name: "vel_session", Value: "", Path: "/", MaxAge: -1}).String())
					panic("settlement failed after the deletion")
				},
				write: func(http.ResponseWriter) {},
			})
			holder.lifecycle.Unlock()
			scheme.Session(c.Request).Put("touched", true)
			return c.String(http.StatusOK, "ok")
		}, sessionEnded},
		{"session store save panics", sessionKept, func(rig *commitPanicRig, scheme *SessionScheme, c *router.Context) error {
			rig.savePanic.Store(true)
			scheme.Session(c.Request).Put("touched", true)
			return c.String(http.StatusOK, "ok")
		}, sessionKept},
	}
	for _, mode := range storeModes {
		for _, tc := range cases {
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				rig := newCommitPanicRig(t, mode.serverSide)
				scheme := rig.instance()
				a := newStoreBrowser(t, scheme)
				var served atomic.Pointer[http.Request]
				handlerRead := make(chan struct{}, 1)
				rt := a.handler.(*router.VelocityRouterV2)
				rt.SetErrorHandler(func(c *router.Context, _ error, info router.ErrorInfo) {
					_ = scheme.User(c.Request)
					handlerRead <- struct{}{}
					if !info.Committed {
						_ = c.String(http.StatusInternalServerError, "failed")
					}
				})
				withRoute(a, "/commit-panic", func(c *router.Context) error {
					served.Store(c.Request)
					return tc.handle(rig, scheme, c)
				})
				var captured map[string]*http.Cookie
				if tc.start == sessionKept {
					captured = signInAndCapture(t, a)
				}

				w := doWithin(t, a, http.MethodGet, "/commit-panic")
				rig.rememberPanic.Store(false)
				rig.savePanic.Store(false)
				select {
				case <-handlerRead:
				case <-time.After(time.Second):
					t.Fatalf("the error handler never read the user (status %d, body %q)", w.Code, w.Body.String())
				}
				if w.Code != http.StatusInternalServerError {
					t.Fatalf("premise: status %d, want the error handler's 500 for the panic", w.Code)
				}
				if req := served.Load(); QueueAfterSessionSave(req, func(http.ResponseWriter) {}) {
					t.Fatal("a write queued after the recovery was accepted, but no delivery will run it")
				}
				switch tc.want {
				case sessionEnded:
					if replaySignsIn(a, captured) {
						t.Fatal("the session whose cookie deletion was staged before the panic still signs in")
					}
					if replaySignsIn(newStoreBrowser(t, rig.instance()), captured) {
						t.Fatal("the session whose cookie deletion was staged before the panic still signs in on another instance")
					}
				case sessionKept:
					if !replaySignsIn(a, captured) {
						t.Fatal("a save that panicked ended the session it never issued")
					}
					if !replaySignsIn(newStoreBrowser(t, rig.instance()), captured) {
						t.Fatal("a save that panicked ended the session it never issued, on another instance")
					}
				}
			})
		}
	}
}
