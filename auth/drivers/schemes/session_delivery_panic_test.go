package schemes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/router"
)

// A write queued behind the session save that panics after an earlier
// write deleted the session cookie still ends the session: the router
// answers the panic with its error response, which carries the response's
// cookies, so the deletion it sends must be matched by the server-side
// teardown. The response carries one session cookie line, the deletion; a
// captured copy of the cookie is refused on this instance and on another
// one sharing the store; and the queue is closed, so a write queued after
// the recovery is refused rather than accepted for a delivery that never
// comes. Covered for a commit the handler's write fires and for the one the
// router fires when the handler wrote nothing.
func TestSessionMiddleware_PanicDuringDeliveryStillEndsADeletedSession(t *testing.T) {
	commits := []struct {
		name string
		// respond ends the handler: it writes the response (the commit
		// fires from the handler) or writes nothing (the router fires the
		// commit once its boundary is done with the request).
		respond func(c *router.Context) error
	}{
		{"commit fired by the handler's write", func(c *router.Context) error {
			return c.String(http.StatusOK, "forgotten")
		}},
		{"commit fired by the router with no body", func(*router.Context) error {
			return nil
		}},
	}
	for _, mode := range storeModes {
		for _, commit := range commits {
			t.Run(mode.name+"/"+commit.name, func(t *testing.T) {
				instance := sharedInstances(t, mode.serverSide)
				scheme := instance()
				a := newStoreBrowser(t, scheme)
				var served atomic.Pointer[http.Request]
				var laterRan atomic.Bool
				withRoute(a, "/forget-then-panic", func(c *router.Context) error {
					served.Store(c.Request)
					QueueAfterSessionSave(c.Request, func(http.ResponseWriter) { c.DeleteCookie("vel_session") })
					QueueAfterSessionSave(c.Request, func(http.ResponseWriter) { panic("queued write failed") })
					QueueAfterSessionSave(c.Request, func(http.ResponseWriter) { laterRan.Store(true) })
					scheme.Session(c.Request).Put("touched", true) // the save issues the cookie again
					return commit.respond(c)
				})
				captured := signInAndCapture(t, a)

				w := doWithin(t, a, http.MethodGet, "/forget-then-panic")
				if w.Code != http.StatusInternalServerError {
					t.Fatalf("premise: status %d, want the router's 500 for the panic", w.Code)
				}
				var lines []string
				for _, line := range w.Result().Header.Values("Set-Cookie") {
					if strings.HasPrefix(line, "vel_session=") {
						lines = append(lines, line)
					}
				}
				if len(lines) != 1 || !strings.Contains(lines[0], "Max-Age=0") {
					t.Fatalf("session cookie lines = %q, want one deletion", lines)
				}
				if laterRan.Load() {
					t.Fatal("a write queued after the panicking one ran")
				}
				if req := served.Load(); QueueAfterSessionSave(req, func(http.ResponseWriter) {}) {
					t.Fatal("a write queued after the recovery was accepted, but no delivery will run it")
				}
				if replaySignsIn(a, captured) {
					t.Fatal("the session whose cookie was deleted before the panic still signs in")
				}
				if replaySignsIn(newStoreBrowser(t, instance()), captured) {
					t.Fatal("the session whose cookie was deleted before the panic still signs in on another instance")
				}
			})
		}
	}
}

// The same holds for a commit outside the session middleware (a Login or
// Logout driven from a plain handler, which commits its own save scope): a
// queued write that panics after an earlier one deleted the session cookie
// leaves one deletion in the response, the later write unrun, the queue
// closed and the session ended on every instance sharing the store.
func TestCommitStandalone_PanicDuringDeliveryStillEndsADeletedSession(t *testing.T) {
	for _, mode := range storeModes {
		t.Run(mode.name, func(t *testing.T) {
			instance := sharedInstances(t, mode.serverSide)
			scheme := instance()
			a := newStoreBrowser(t, scheme)
			captured := signInAndCapture(t, a)

			r := httptest.NewRequest(http.MethodGet, "/standalone", nil)
			for _, c := range captured {
				r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
			}
			w := httptest.NewRecorder()
			holder, standalone := seamHolder(r)
			if !standalone {
				t.Fatal("premise: a request outside the middleware is not its own save scope")
			}
			var laterRan bool
			holder.lifecycle.Lock()
			s := scheme.Session(r)
			if s == nil {
				t.Fatal("premise: the captured cookie loads no session")
			}
			holder.setSession(s)
			s.Put("touched", true)
			holder.queueCredentialWrite(afterSaveWrite{write: func(w http.ResponseWriter) {
				http.SetCookie(w, &http.Cookie{Name: "vel_session", Value: "", Path: "/", MaxAge: -1})
			}})
			holder.queueCredentialWrite(afterSaveWrite{write: func(http.ResponseWriter) { panic("queued write failed") }})
			holder.queueCredentialWrite(afterSaveWrite{write: func(http.ResponseWriter) { laterRan = true }})
			func() {
				defer holder.lifecycle.Unlock()
				defer func() {
					if recover() == nil {
						t.Fatal("premise: the queued write's panic did not reach the caller")
					}
				}()
				_ = commitStandalone(scheme, r, w, holder)
			}()

			var lines []string
			for _, line := range w.Result().Header.Values("Set-Cookie") {
				if strings.HasPrefix(line, "vel_session=") {
					lines = append(lines, line)
				}
			}
			if len(lines) != 1 || !strings.Contains(lines[0], "Max-Age=0") {
				t.Fatalf("session cookie lines = %q, want one deletion", lines)
			}
			if laterRan {
				t.Fatal("a write queued after the panicking one ran")
			}
			if holder.queueAfterSave(func(http.ResponseWriter) {}) {
				t.Fatal("a write queued after the panic was accepted, but no delivery will run it")
			}
			if replaySignsIn(a, captured) {
				t.Fatal("the session whose cookie was deleted before the panic still signs in")
			}
			if replaySignsIn(newStoreBrowser(t, instance()), captured) {
				t.Fatal("the session whose cookie was deleted before the panic still signs in on another instance")
			}
		})
	}
}
