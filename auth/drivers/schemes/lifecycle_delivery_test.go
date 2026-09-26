package schemes

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/router"
)

// serveWithin runs b.do on its own goroutine and fails the test when the
// request has not returned within a generous bound: a request stuck on
// a lock never returns, whatever the bound.
func serveWithin(t *testing.T, b *rememberBrowser, method, path string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.do(method, path)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s %s never returned: the response commit is stuck", method, path)
	}
}

// A write queued behind the session save may read the signed-in user of
// the same request: the seam delivers the queued writes after it released
// the request's lifecycle lock, so the read neither waits for the commit
// that runs it nor sees anything but the saved sign-in.
func TestSessionMiddleware_QueuedWriteReadsTheSignedInUser(t *testing.T) {
	for _, mode := range lifetimeModes {
		t.Run(mode.name, func(t *testing.T) {
			installLifetimeClock(t)
			scheme, _ := newLifetimeSchemeFor(t, 120, 0, mode)
			var seen atomic.Value
			b := transitionBrowser(t, scheme, "/profile", func(c *router.Context) error {
				req := c.Request
				if !QueueAfterSessionSave(req, func(w http.ResponseWriter) {
					if u := scheme.User(req); u != nil {
						seen.Store(u.GetAuthIdentifier())
					}
				}) {
					t.Error("QueueAfterSessionSave refused a request inside the session middleware")
				}
				return c.String(http.StatusOK, "profile")
			})
			b.do(http.MethodPost, "/login")

			serveWithin(t, b, http.MethodGet, "/profile")
			if got, _ := seen.Load().(string); got != "u1" {
				t.Fatalf("queued write read user %q, want u1", got)
			}
			if !b.signedIn() {
				t.Fatalf("session no longer signed in after the queued read (err=%v)", b.lastErr)
			}
		})
	}
}

// Once the seam saved the session, the request's sign-in state is sealed:
// a Logout run while the queued writes are delivered (here by a write
// queued ahead of the remember-me sign-in's) still ends the remember
// credential the sign-in minted, since the mint happened under the lock
// before the seal, and nothing the browser holds signs in afterwards. A
// sign-in attempted at that point is refused and changes nothing.
func TestSessionMiddleware_TransitionDuringDeliveryCannotRestoreTheCredential(t *testing.T) {
	for _, mode := range lifetimeModes {
		t.Run(mode.name, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, _ := newLifetimeSchemeFor(t, 120, 0, mode)
			users := userStoreOf(t, scheme)
			var logoutErr, loginErr atomic.Value
			b := transitionBrowser(t, scheme, "/switch", func(c *router.Context) error {
				req := c.Request
				QueueAfterSessionSave(req, func(w http.ResponseWriter) {
					if err := scheme.Logout(w, req); err != nil {
						logoutErr.Store(err)
					}
					if err := scheme.Login(w, req, &revokeTestUser{id: "u1"}, true); err != nil {
						loginErr.Store(err)
					}
				})
				if err := scheme.Login(c.Response, req, &revokeTestUser{id: "u1"}, true); err != nil {
					return err
				}
				return c.String(http.StatusOK, "done")
			})

			serveWithin(t, b, http.MethodPost, "/switch")
			if err, _ := logoutErr.Load().(error); err != nil {
				t.Fatalf("Logout during delivery: %v", err)
			}
			if err, _ := loginErr.Load().(error); err == nil {
				t.Error("a sign-in after the session was saved was accepted")
			}
			if got := users.token("u1"); got != "" {
				t.Fatalf("remember token stored after the logout that ran during delivery: %q", got)
			}
			rem := b.cookies[rememberCookieName]
			if b.signedIn() {
				t.Fatal("the session is still signed in after the logout that ran during delivery")
			}
			if rem != nil {
				clock.advance(time.Minute)
				b.replayRememberOnly(*rem)
				if b.signedIn() {
					t.Fatal("the remember cookie delivered after the logout signed in on its own")
				}
			}
		})
	}
}

// deleteFailingRecords is a server session store whose deletes fail while
// down is set.
type deleteFailingRecords struct {
	auth.ServerSessionStore
	down atomic.Bool
}

var errRecordDeleteFailed = errors.New("session record delete failed")

func (s *deleteFailingRecords) Delete(ctx context.Context, id string) error {
	if s.down.Load() {
		return errRecordDeleteFailed
	}
	return s.ServerSessionStore.Delete(ctx, id)
}

// A sign-in that aborts before it replaced the session (the previous
// record could not be retired) supersedes nothing: the remember-me recall
// that ran earlier in the request keeps its writes and its undo. When the
// save succeeds the browser gets the rotated remember cookie and the
// recall's XSRF-TOKEN; when it fails the stored token is restored to the
// presented one, which still signs in.
func TestLogin_AbortedSignInKeepsTheRecallDeliveryAndUndo(t *testing.T) {
	for _, saveFails := range []bool{false, true} {
		for _, mode := range rememberModes {
			name := mode.name + "/save succeeds"
			if saveFails {
				name = mode.name + "/save fails"
			}
			t.Run(name, func(t *testing.T) {
				clock := installLifetimeClock(t)
				scheme, mem := newLifetimeSchemeFor(t, 120, 0, mode)
				users := userStoreOf(t, scheme)
				records := &deleteFailingRecords{ServerSessionStore: mem}
				scheme.SetServerSessionStore(records)
				rotator := &fakeCSRFRotator{}
				scheme.SetCSRFTokenRotator(rotator)

				var recalledID, loginErr atomic.Value
				b := transitionBrowser(t, scheme, "/recall", func(c *router.Context) error {
					req := c.Request
					if scheme.User(req) == nil {
						t.Error("premise: the remember cookie did not recall")
					}
					if s := sessionFromHolder(req); s != nil {
						recalledID.Store(s.ID())
					}
					records.down.Store(true)
					err := scheme.Login(c.Response, req, &revokeTestUser{id: "u1"}, false)
					records.down.Store(false)
					if err != nil {
						loginErr.Store(err)
					}
					return c.String(http.StatusOK, "done")
				})

				b.do(http.MethodPost, "/login")
				presented := *b.cookies[rememberCookieName]
				stored := users.token("u1")
				b.replayRememberOnly(presented)
				clock.advance(time.Minute)
				rotator.mu.Lock()
				rotator.xsrfWrote = nil
				rotator.mu.Unlock()

				if saveFails {
					orig := saveSessionFromMiddleware
					saveSessionFromMiddleware = func(*SessionScheme, http.ResponseWriter, auth.Session) error {
						return errors.New("store down")
					}
					b.do(http.MethodGet, "/recall")
					saveSessionFromMiddleware = orig
				} else {
					b.do(http.MethodGet, "/recall")
				}
				if err, _ := loginErr.Load().(error); !errors.Is(err, errRecordDeleteFailed) {
					t.Fatalf("premise: Login returned %v, want the retirement failure", err)
				}
				id, _ := recalledID.Load().(string)
				rotator.mu.Lock()
				wrote := append([]string(nil), rotator.xsrfWrote...)
				rotator.mu.Unlock()

				if saveFails {
					if got := users.token("u1"); got != stored {
						t.Fatalf("stored remember token after the failed save: %q, want the presented token's %q restored", got, stored)
					}
					if len(wrote) != 0 {
						t.Errorf("XSRF-TOKEN written for a session that was not saved: %v", wrote)
					}
					b.replayRememberOnly(presented)
					if !b.signedIn() {
						t.Fatalf("presented remember cookie no longer signs in after the failed save (err=%v)", b.lastErr)
					}
					return
				}

				rem := b.liveCookie(rememberCookieName)
				if rem == nil {
					t.Fatal("the recall's rotated remember cookie was not delivered")
				}
				if got := users.token("u1"); got == stored {
					t.Fatal("premise: the recall did not rotate the stored token")
				}
				if len(wrote) != 1 || wrote[0] != id {
					t.Errorf("XSRF-TOKEN writes %v, want the recalled session's %q", wrote, id)
				}
				if !b.signedIn() {
					t.Fatalf("recalled session not signed in (err=%v)", b.lastErr)
				}
				clock.advance(time.Minute)
				b.replayRememberOnly(*rem)
				if !b.signedIn() {
					t.Fatalf("delivered remember cookie does not sign in: the browser and the store disagree (err=%v)", b.lastErr)
				}
			})
		}
	}
}

// A failed save persists nothing of the request, so it reverses every
// change made outside the session for it, including a remember-me
// recall's rotation that a later sign-in of the request superseded: the
// presented remember cookie still signs in once saves work again.
func TestLogin_FailedSaveUndoesASupersededRecall(t *testing.T) {
	for _, mode := range rememberModes {
		t.Run(mode.name, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, _ := newLifetimeSchemeFor(t, 120, 0, mode)
			users := userStoreOf(t, scheme)
			b := transitionBrowser(t, scheme, "/recall", func(c *router.Context) error {
				if scheme.User(c.Request) == nil {
					t.Error("premise: the remember cookie did not recall")
				}
				if err := scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}, false); err != nil {
					t.Errorf("premise: sign-in after the recall: %v", err)
				}
				return c.String(http.StatusOK, "done")
			})
			b.do(http.MethodPost, "/login")
			presented := *b.cookies[rememberCookieName]
			stored := users.token("u1")
			b.replayRememberOnly(presented)
			clock.advance(time.Minute)

			orig := saveSessionFromMiddleware
			saveSessionFromMiddleware = func(*SessionScheme, http.ResponseWriter, auth.Session) error {
				return errors.New("store down")
			}
			b.do(http.MethodGet, "/recall")
			saveSessionFromMiddleware = orig

			if got := users.token("u1"); got != stored {
				t.Fatalf("stored remember token after the failed save: %q, want the presented token's %q restored", got, stored)
			}
			b.replayRememberOnly(presented)
			if !b.signedIn() {
				t.Fatalf("presented remember cookie no longer signs in after the failed save (err=%v)", b.lastErr)
			}
		})
	}
}
