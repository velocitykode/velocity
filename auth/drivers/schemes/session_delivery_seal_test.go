package schemes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/csrf"
	csrfstores "github.com/velocitykode/velocity/csrf/stores"
	"github.com/velocitykode/velocity/router"
)

// sharedInstances builds instances of one application sharing a server
// session store: with serverSide the session lives in that store's
// records, otherwise in the cookie with the store as its sign-in index.
func sharedInstances(t *testing.T, serverSide bool) func() *SessionScheme {
	t.Helper()
	cfg := teardownConfig()
	enc := teardownEncryptor(t)
	shared := session.NewMemoryStore()
	users := &revokeTestStore{users: map[string]*revokeTestUser{"u1": {id: "u1"}}}
	return func() *SessionScheme {
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
}

// storeModes names the two built-in session stores.
var storeModes = []struct {
	name       string
	serverSide bool
}{
	{"cookie store", false},
	{"server store", true},
}

// withRoute adds a GET route to b's router.
func withRoute(b *storeBrowser, path string, h router.HandlerFunc) {
	b.handler.(*router.VelocityRouterV2).Get(path, h)
}

// doWithin runs b.do on its own goroutine and fails the test when the
// request has not returned within a generous bound: a request stuck on a
// lock never returns, whatever the bound.
func doWithin(t *testing.T, b *storeBrowser, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- b.do(method, path) }()
	select {
	case w := <-done:
		return w
	case <-time.After(10 * time.Second):
		t.Fatalf("%s %s never returned: the response commit is stuck", method, path)
		return nil
	}
}

// replaySignsIn reports whether the cookies captured signs in on b.
func replaySignsIn(b *storeBrowser, captured map[string]*http.Cookie) bool {
	b.cookies = map[string]*http.Cookie{}
	for k, v := range captured {
		b.cookies[k] = v
	}
	return b.do(http.MethodGet, "/check").Body.String() == "in"
}

// signInAndCapture signs b in and returns a copy of the cookies it holds.
func signInAndCapture(t *testing.T, b *storeBrowser) map[string]*http.Cookie {
	t.Helper()
	b.do(http.MethodPost, "/login")
	if _, ok := b.cookies["vel_session"]; !ok {
		t.Fatal("premise: no session cookie after the sign-in")
	}
	captured := map[string]*http.Cookie{}
	for k, v := range b.cookies {
		captured[k] = v
	}
	return captured
}

// A write queued behind the session save runs after the request's one
// save: the session it can reach is sealed, so it cannot move the session
// to an id no response delivers, and a Logout it runs ends the session the
// save issued. The cookie the browser holds is refused afterwards, on this
// instance and on another one sharing the store.
func TestSessionMiddleware_LogoutAfterTheSaveEndsTheIssuedSession(t *testing.T) {
	for _, mode := range storeModes {
		t.Run(mode.name, func(t *testing.T) {
			instance := sharedInstances(t, mode.serverSide)
			scheme := instance()
			a := newStoreBrowser(t, scheme)
			var regenErr, logoutErr atomic.Value
			withRoute(a, "/late-logout", func(c *router.Context) error {
				req := c.Request
				QueueAfterSessionSave(req, func(w http.ResponseWriter) {
					if err := scheme.Session(req).Regenerate(); err != nil {
						regenErr.Store(err)
					}
					if err := scheme.Logout(w, req); err != nil {
						logoutErr.Store(err)
					}
				})
				return c.String(http.StatusOK, "done")
			})
			captured := signInAndCapture(t, a)

			a.do(http.MethodGet, "/late-logout")
			if err, _ := logoutErr.Load().(error); err != nil {
				t.Fatalf("Logout during delivery: %v", err)
			}
			if replaySignsIn(a, captured) {
				t.Fatal("the session cookie the save issued still signs in after the logout that followed it")
			}
			if replaySignsIn(newStoreBrowser(t, instance()), captured) {
				t.Fatal("the session cookie the save issued still signs in on another instance after the logout")
			}
			if err, _ := regenErr.Load().(error); !errors.Is(err, auth.ErrSessionSealed) {
				t.Fatalf("Regenerate after the save returned %v, want auth.ErrSessionSealed", err)
			}
		})
	}
}

// A write queued behind the session save that deletes the session cookie
// ends the session as a handler's deletion does: the response carries one
// session cookie line, the deletion, and a captured copy of the cookie is
// refused on this instance and on another one sharing the store.
func TestSessionMiddleware_DeletingTheSessionCookieDuringDeliveryEndsTheSession(t *testing.T) {
	deletions := []struct {
		name string
		// queue queues the deletion on the request c serves.
		queue func(c *router.Context)
	}{
		{"through the router context", func(c *router.Context) {
			QueueAfterSessionSave(c.Request, func(http.ResponseWriter) { c.DeleteCookie("vel_session") })
		}},
		{"through the queued write's writer", func(c *router.Context) {
			QueueAfterSessionSave(c.Request, func(w http.ResponseWriter) {
				w.Header().Add("Set-Cookie", "vel_session=; Path=/; Max-Age=0")
			})
		}},
	}
	for _, mode := range storeModes {
		for _, del := range deletions {
			t.Run(mode.name+"/"+del.name, func(t *testing.T) {
				instance := sharedInstances(t, mode.serverSide)
				scheme := instance()
				a := newStoreBrowser(t, scheme)
				withRoute(a, "/forget-late", func(c *router.Context) error {
					del.queue(c)
					scheme.Session(c.Request).Put("touched", true) // the save issues the cookie again
					return c.String(http.StatusOK, "forgotten")
				})
				captured := signInAndCapture(t, a)

				w := a.do(http.MethodGet, "/forget-late")
				var lines []string
				for _, line := range w.Result().Header.Values("Set-Cookie") {
					if strings.HasPrefix(line, "vel_session=") {
						lines = append(lines, line)
					}
				}
				if len(lines) != 1 || !strings.Contains(lines[0], "Max-Age=0") {
					t.Fatalf("session cookie lines = %q, want one deletion", lines)
				}
				if replaySignsIn(a, captured) {
					t.Fatal("the session whose cookie was deleted during delivery still signs in")
				}
				if replaySignsIn(newStoreBrowser(t, instance()), captured) {
					t.Fatal("the session whose cookie was deleted during delivery still signs in on another instance")
				}
			})
		}
	}
}

// A queued write gets the response's headers and nothing else: a body
// write or a status it attempts is refused, so it can neither commit the
// response from inside the commit (which would never return) nor replace
// what the handler writes, and the cookies it adds are still delivered.
func TestSessionMiddleware_QueuedWriteCannotWriteTheResponse(t *testing.T) {
	for _, mode := range storeModes {
		t.Run(mode.name, func(t *testing.T) {
			instance := sharedInstances(t, mode.serverSide)
			a := newStoreBrowser(t, instance())
			var writeErr atomic.Value
			withRoute(a, "/late-body", func(c *router.Context) error {
				QueueAfterSessionSave(c.Request, func(w http.ResponseWriter) {
					w.Header().Add("Set-Cookie", "late=1; Path=/")
					w.WriteHeader(http.StatusTeapot)
					if _, err := w.Write([]byte("from the queued write")); err != nil {
						writeErr.Store(err)
					}
				})
				return c.String(http.StatusOK, "from the handler")
			})
			signInAndCapture(t, a)

			w := doWithin(t, a, http.MethodGet, "/late-body")
			if w.Code != http.StatusOK || w.Body.String() != "from the handler" {
				t.Fatalf("response = %d %q, want the handler's 200 %q", w.Code, w.Body.String(), "from the handler")
			}
			if err, _ := writeErr.Load().(error); err == nil {
				t.Fatal("the queued write's body write reported success")
			}
			if !strings.Contains(strings.Join(w.Result().Header.Values("Set-Cookie"), "\n"), "late=1") {
				t.Fatal("the cookie the queued write added was not delivered")
			}
			if a.do(http.MethodGet, "/check").Body.String() != "in" {
				t.Fatalf("the session is no longer signed in (err=%v)", a.lastErr)
			}
		})
	}
}

// Registration behind the session save is honest: a write queued while
// the queued writes are delivered runs as part of that delivery, and a
// write queued once the delivery finished (after the response's first
// write) is refused, since nothing would run it.
func TestQueueAfterSessionSave_RegistrationFollowsTheDelivery(t *testing.T) {
	scheme := sharedInstances(t, false)()
	a := newStoreBrowser(t, scheme)
	var nestedQueued, lateQueued, lateRan atomic.Bool
	withRoute(a, "/nested", func(c *router.Context) error {
		req := c.Request
		QueueAfterSessionSave(req, func(http.ResponseWriter) {
			nestedQueued.Store(QueueAfterSessionSave(req, func(w http.ResponseWriter) {
				w.Header().Add("Set-Cookie", "nested=1; Path=/")
			}))
		})
		return c.String(http.StatusOK, "nested")
	})
	withRoute(a, "/after-write", func(c *router.Context) error {
		err := c.String(http.StatusOK, "written")
		lateQueued.Store(QueueAfterSessionSave(c.Request, func(http.ResponseWriter) { lateRan.Store(true) }))
		return err
	})
	signInAndCapture(t, a)

	w := a.do(http.MethodGet, "/nested")
	if !nestedQueued.Load() {
		t.Fatal("a write queued during the delivery was refused")
	}
	if !strings.Contains(strings.Join(w.Result().Header.Values("Set-Cookie"), "\n"), "nested=1") {
		t.Fatal("a write queued during the delivery never ran")
	}

	a.do(http.MethodGet, "/after-write")
	if lateQueued.Load() {
		t.Fatal("a write queued after the delivery finished was accepted")
	}
	if lateRan.Load() {
		t.Fatal("a write queued after the delivery finished ran")
	}
}

// A CSRF token rotation asked for after the request's session was saved
// is refused: the token lives in the session, and a rotation nothing saves
// would leave the token the client holds valid while reporting success.
func TestCSRFRotateToken_AfterTheSaveIsRefused(t *testing.T) {
	for _, mode := range storeModes {
		t.Run(mode.name, func(t *testing.T) {
			scheme := sharedInstances(t, mode.serverSide)()
			cfg := csrf.DefaultConfig()
			cfg.CookiePolicy = contract.NewCookiePolicy("/", "", false, http.SameSiteLaxMode)
			cfg.Store = csrfstores.NewSessionBagStore(func(ctx context.Context) csrfstores.SessionBag {
				if s := SessionFromContext(ctx); s != nil {
					return s
				}
				return nil
			}, 0)
			cfg.SessionIDResolver = func(r *http.Request) (string, error) {
				s, err := scheme.ResolveSession(r)
				if err != nil {
					return "", csrf.ErrNoSession
				}
				return s.ID(), nil
			}
			protector, err := csrf.NewE(cfg)
			if err != nil {
				t.Fatalf("csrf.NewE: %v", err)
			}
			a := newStoreBrowser(t, scheme)
			var before, after atomic.Value
			withRoute(a, "/late-rotate", func(c *router.Context) error {
				req := c.Request
				id := scheme.Session(req).ID()
				before.Store(fmt.Sprint(protector.RotateToken(req.Context(), id, id)))
				QueueAfterSessionSave(req, func(http.ResponseWriter) {
					after.Store(fmt.Sprint(protector.RotateToken(req.Context(), id, id)))
				})
				return c.String(http.StatusOK, "done")
			})
			signInAndCapture(t, a)

			a.do(http.MethodGet, "/late-rotate")
			if got := before.Load(); got != "<nil>" {
				t.Fatalf("premise: rotation before the save returned %v", got)
			}
			if got := after.Load(); got == "<nil>" {
				t.Fatal("a rotation after the session was saved reported success")
			}
		})
	}
}
