package schemes

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/cache/drivers"
	"github.com/velocitykode/velocity/router"
)

// rememberModes are the session stores a remembered visitor can be
// revived on when a server store is installed: the session in the cookie
// with the server record as its revocation index, and the session in the
// server record behind an id cookie.
var rememberModes = []lifetimeMode{lifetimeModes[0], lifetimeModes[2]}

// replayRememberOnly makes b send only the given remember cookie, by hand,
// whatever its Max-Age said: an attacker replaying a captured cookie.
func (b *rememberBrowser) replayRememberOnly(rem http.Cookie) {
	b.cookies = map[string]*http.Cookie{rememberCookieName: &rem}
	b.expires = map[string]time.Time{}
}

// A server record deleted outside Manager (the store's own Delete) is never
// revived by the remember cookie the device still holds: the request is
// refused as revoked, the presented remember credential is burned, and
// replaying it alone afterwards signs nobody in.
func TestRememberRevocation_DeletedRecordIsNeverRevived(t *testing.T) {
	for _, mode := range rememberModes {
		t.Run(mode.name, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, mem := newLifetimeSchemeFor(t, 120, 0, mode)
			users := userStoreOf(t, scheme)
			b := newRememberBrowser(t, scheme)
			b.do(http.MethodPost, "/login")
			stolen := *b.cookies[rememberCookieName]

			if err := mem.Delete(context.Background(), onlyRecord(t, mem, "").ID); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			clock.advance(time.Minute)
			if b.signedIn() {
				t.Fatal("deleted session record revived by the remember cookie")
			}
			if !errors.Is(b.lastErr, auth.ErrSessionRevoked) {
				t.Fatalf("deleted session reported as %v, want auth.ErrSessionRevoked", b.lastErr)
			}
			if got := users.token("u1"); got != "" {
				t.Fatalf("remember token still stored after the deleted session presented it: %q", got)
			}

			b.replayRememberOnly(stolen)
			if b.signedIn() {
				t.Fatal("remember cookie of a deleted session signed in on its own")
			}
		})
	}
}

// Manager.RevokeSession ends the remember credential of the revoked
// session's owner, so a captured remember cookie replayed without the
// revoked session cookie signs nobody in.
func TestRememberRevocation_RevokeSessionEndsRememberOnlyReplay(t *testing.T) {
	for _, mode := range rememberModes {
		t.Run(mode.name, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, mem := newLifetimeSchemeFor(t, 120, 0, mode)
			mgr := auth.NewManager()
			mgr.SetServerSessionStore(mem)
			mgr.RegisterScheme("web", scheme)
			b := newRememberBrowser(t, scheme)
			b.do(http.MethodPost, "/login")
			stolen := *b.cookies[rememberCookieName]

			if err := mgr.RevokeSession(context.Background(), onlyRecord(t, mem, "").ID); err != nil {
				t.Fatalf("RevokeSession: %v", err)
			}
			clock.advance(time.Minute)
			b.replayRememberOnly(stolen)
			if b.signedIn() {
				t.Fatal("remember cookie alone signed in after RevokeSession of its session")
			}
		})
	}
}

// The remember credential ends on the server after RememberLifetime from
// its issue, whatever the cookie jar does: a captured cookie replayed by
// hand past its lifetime signs nobody in, one replayed inside it does.
func TestRememberCredential_ExpiresOnTheServer(t *testing.T) {
	tests := []struct {
		name     string
		after    time.Duration
		signedIn bool
	}{
		{name: "inside the remember lifetime", after: 23 * time.Hour, signedIn: true},
		{name: "past the remember lifetime", after: 25 * time.Hour, signedIn: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, _ := newLifetimeScheme(t, 120, 0, false)
			scheme.config.RememberLifetime = 24 * 60
			b := newRememberBrowser(t, scheme)
			b.do(http.MethodPost, "/login")
			stolen := *b.cookies[rememberCookieName]

			clock.advance(tt.after)
			b.replayRememberOnly(stolen)
			if got := b.signedIn(); got != tt.signedIn {
				t.Fatalf("remember cookie replayed %v after issue: signed in = %v, want %v", tt.after, got, tt.signedIn)
			}
		})
	}
}

// Many goroutines reading the user on one request of a remembered visitor
// whose session ended all see the user, and the recall happens once: one
// new session carrying the user, one rotated remember cookie, and the
// next request is signed in on the saved session.
func TestRememberRecall_ConcurrentReadsOnOneRequestRecallOnce(t *testing.T) {
	for _, mode := range lifetimeModes {
		t.Run(mode.name, func(t *testing.T) {
			installLifetimeClock(t)
			scheme, _ := newLifetimeSchemeFor(t, 120, 0, mode)
			b := newRememberBrowser(t, scheme)
			const readers = 16
			var denied int
			r := router.New()
			r.Use(scheme.SessionMiddleware())
			r.Post("/login", func(c *router.Context) error {
				if err := scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}, true); err != nil {
					return err
				}
				return c.String(http.StatusOK, "in")
			})
			r.Get("/fanout", func(c *router.Context) error {
				var (
					wg sync.WaitGroup
					mu sync.Mutex
				)
				denied = 0
				wg.Add(readers)
				for i := 0; i < readers; i++ {
					go func() {
						defer wg.Done()
						if scheme.User(c.Request) == nil {
							mu.Lock()
							denied++
							mu.Unlock()
						}
					}()
				}
				wg.Wait()
				return c.String(http.StatusOK, "done")
			})
			r.Get("/check", func(c *router.Context) error {
				ok, err := scheme.CheckWithError(c.Request)
				b.lastErr = err
				if !ok {
					return c.String(http.StatusUnauthorized, "out")
				}
				return c.String(http.StatusOK, "in")
			})
			b.handler = r

			b.do(http.MethodPost, "/login")
			b.replayRememberOnly(*b.cookies[rememberCookieName])

			w := b.do(http.MethodGet, "/fanout")
			if denied != 0 {
				t.Fatalf("%d of %d concurrent reads on the remembered visitor's request saw no user", denied, readers)
			}
			var remembers, sessions int
			for _, c := range w.Result().Cookies() {
				switch c.Name {
				case rememberCookieName:
					remembers++
				case sessionCookieName:
					sessions++
				}
			}
			if remembers != 1 || sessions != 1 {
				t.Fatalf("recall wrote %d remember and %d session cookies, want 1 each", remembers, sessions)
			}
			if !b.signedIn() {
				t.Fatalf("next request not signed in on the recalled session (err=%v)", b.lastErr)
			}
		})
	}
}

// A recall whose session save fails sends no rotated remember cookie and
// leaves the presented remember credential valid, so the visitor is not
// locked out: the same cookie signs in once saving works again.
func TestRememberRecall_FailedSaveKeepsThePresentedCredential(t *testing.T) {
	for _, mode := range lifetimeModes {
		t.Run(mode.name, func(t *testing.T) {
			installLifetimeClock(t)
			scheme, _ := newLifetimeSchemeFor(t, 120, 0, mode)
			users := userStoreOf(t, scheme)
			b := newRememberBrowser(t, scheme)
			b.do(http.MethodPost, "/login")
			presented := *b.cookies[rememberCookieName]
			stored := users.token("u1")

			orig := saveSessionFromMiddleware
			saveSessionFromMiddleware = func(*SessionScheme, http.ResponseWriter, auth.Session) error {
				return errors.New("store down")
			}
			b.replayRememberOnly(presented)
			b.do(http.MethodGet, "/check")
			saveSessionFromMiddleware = orig

			if rem := b.responseCookie(rememberCookieName); rem != nil {
				t.Fatalf("rotated remember cookie sent for a session that was not saved: %+v", rem)
			}
			if got := users.token("u1"); got != stored {
				t.Fatalf("stored remember token moved although the recall's save failed: %q, want %q", got, stored)
			}
			b.replayRememberOnly(presented)
			if !b.signedIn() {
				t.Fatalf("presented remember cookie no longer signs in after the failed save (err=%v)", b.lastErr)
			}
		})
	}
}

// A server-held session whose record the cache backend evicted (its TTL
// ran out) cannot be told from a revoked one: the record was the only
// authority, so the request is reported as revoked and a remember
// credential it presents is burned, never used to revive it.
func TestServerStore_EvictedRecordReportsRevoked(t *testing.T) {
	backend := drivers.NewMemoryStore("sessions")
	t.Cleanup(func() { _ = backend.Shutdown(context.Background()) })
	records, err := session.NewCacheStore(backend)
	if err != nil {
		t.Fatalf("NewCacheStore: %v", err)
	}
	scheme, _ := newLifetimeScheme(t, 120, 0, false)
	scheme.SetServerSessionStore(records)
	st, err := session.NewServerStore(scheme.config, records)
	if err != nil {
		t.Fatal(err)
	}
	scheme.store = st
	users := userStoreOf(t, scheme)
	b := newRememberBrowser(t, scheme)
	b.do(http.MethodPost, "/login")
	id := b.cookies[sessionCookieName].Value

	// Shorten the record's life to the backend's one-second TTL floor and
	// let the backend evict it.
	now := time.Now()
	if err := records.Touch(context.Background(), id, now, now.Add(time.Millisecond)); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := records.Get(context.Background(), id); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("record after its TTL: %v, want the backend to have evicted it (auth.ErrSessionNotFound)", err)
	}

	if b.signedIn() {
		t.Fatal("evicted session record revived")
	}
	if !errors.Is(b.lastErr, auth.ErrSessionRevoked) {
		t.Fatalf("evicted record reported as %v, want auth.ErrSessionRevoked", b.lastErr)
	}
	if got := users.token("u1"); got != "" {
		t.Fatalf("remember token still stored after the evicted session presented it: %q", got)
	}
}

// token returns the remember token stored for id.
func (p *revokeTestStore) token(id string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if u, ok := p.users[id]; ok {
		return u.rememberToken
	}
	return ""
}
