package schemes

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/router"
)

// transitionBrowser is a rememberBrowser whose router also serves route,
// for tests that drive one request through several scheme operations.
func transitionBrowser(t *testing.T, scheme *SessionScheme, route string, h router.HandlerFunc) *rememberBrowser {
	t.Helper()
	b := newRememberBrowser(t, scheme)
	r := router.New()
	r.Use(scheme.SessionMiddleware())
	r.Post("/login", func(c *router.Context) error {
		if err := scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}, true); err != nil {
			return err
		}
		return c.String(http.StatusOK, "in")
	})
	r.Get("/check", func(c *router.Context) error {
		ok, err := scheme.CheckWithError(c.Request)
		b.lastErr = err
		if !ok {
			return c.String(http.StatusUnauthorized, "out")
		}
		return c.String(http.StatusOK, "in")
	})
	r.Post(route, h)
	r.Get(route, h)
	b.handler = r
	return b
}

// liveCookie returns the named cookie the last response set with a
// positive Max-Age, or nil.
func (b *rememberBrowser) liveCookie(name string) *http.Cookie {
	for _, c := range b.last.Result().Cookies() {
		if c.Name == name && c.MaxAge > 0 {
			return c
		}
	}
	return nil
}

// Every sign-in operation a request performs after an earlier one
// supersedes the earlier one's credential writes: a remember-me sign-in
// followed by a logout (or by a sign-in without remember-me) in the same
// request leaves no remember credential, stored or delivered, and nothing
// the browser holds afterwards signs in on its own.
func TestLogin_LaterTransitionInTheSameRequestCancelsItsRememberCredential(t *testing.T) {
	tests := []struct {
		name  string
		after func(c *router.Context, scheme *SessionScheme) error
		// signedIn is whether the request ends signed in (on its session).
		signedIn bool
	}{
		{
			name: "logout",
			after: func(c *router.Context, scheme *SessionScheme) error {
				return scheme.Logout(c.Response, c.Request)
			},
		},
		{
			name: "sign-in without remember-me",
			after: func(c *router.Context, scheme *SessionScheme) error {
				return scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}, false)
			},
			signedIn: true,
		},
	}
	for _, tt := range tests {
		for _, mode := range lifetimeModes {
			t.Run(tt.name+"/"+mode.name, func(t *testing.T) {
				clock := installLifetimeClock(t)
				scheme, _ := newLifetimeSchemeFor(t, 120, 0, mode)
				users := userStoreOf(t, scheme)
				b := transitionBrowser(t, scheme, "/switch", func(c *router.Context) error {
					if err := scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}, true); err != nil {
						return err
					}
					if err := tt.after(c, scheme); err != nil {
						return err
					}
					return c.String(http.StatusOK, "done")
				})

				b.do(http.MethodPost, "/switch")
				if got := users.token("u1"); got != "" {
					t.Fatalf("remember token stored after the %s that followed the remember-me sign-in: %q", tt.name, got)
				}
				rem := b.liveCookie(rememberCookieName)
				if rem != nil {
					t.Errorf("response delivered a live remember cookie after the %s", tt.name)
				}
				if got := b.signedIn(); got != tt.signedIn {
					t.Fatalf("after the request: signed in = %v, want %v (err=%v)", got, tt.signedIn, b.lastErr)
				}
				if rem != nil {
					clock.advance(time.Minute)
					b.replayRememberOnly(*rem)
					if b.signedIn() {
						t.Fatalf("remember cookie from a sign-in the %s superseded signed in on its own", tt.name)
					}
				}
			})
		}
	}
}

// pausingCASStore pauses the first remember-token compare-and-swap until
// release is closed, then either lets it land or makes it lose to a
// rotation that happened elsewhere. It lets a test hold a remember-me
// recall in the middle of its transition.
type pausingCASStore struct {
	*revokeTestStore
	entered chan struct{}
	release chan struct{}
	lose    bool
	once    sync.Once
}

func (p *pausingCASStore) CompareAndSwapRememberToken(ctx context.Context, u auth.Authenticatable, oldToken, newToken string) (bool, error) {
	first := false
	p.once.Do(func() { first = true })
	if !first {
		return p.revokeTestStore.CompareAndSwapRememberToken(ctx, u, oldToken, newToken)
	}
	close(p.entered)
	<-p.release
	if p.lose {
		p.mu.Lock()
		p.users["u1"].rememberToken = "rotated-by-another-request"
		p.mu.Unlock()
		return false, nil
	}
	return p.revokeTestStore.CompareAndSwapRememberToken(ctx, u, oldToken, newToken)
}

// parkedIn reports whether some goroutine is blocked on a sync primitive
// with fn on its stack.
func parkedIn(fn string) bool {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	for _, g := range strings.Split(string(buf), "\n\n") {
		header, _, _ := strings.Cut(g, "\n")
		if strings.Contains(header, "[sync.") && strings.Contains(g, fn) {
			return true
		}
	}
	return false
}

// waitReturnedOrParked waits until done is closed or a goroutine running
// fn is blocked on a lock, whichever comes first.
func waitReturnedOrParked(t *testing.T, done <-chan struct{}, fn string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case <-done:
			return
		default:
		}
		if parkedIn(fn) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s neither returned nor blocked", fn)
		}
		runtime.Gosched()
	}
}

// A remember-me recall is one transition: while it is in flight (paused at
// its remember-token compare-and-swap), another reader of the same request
// does not see the provisional user, and the response committed meanwhile
// waits for it. When the swap then loses, nothing of the recall survives:
// both readers see no user and the saved session is signed out.
func TestRememberRecall_ReadersAndCommitWaitForTheTransition(t *testing.T) {
	for _, mode := range lifetimeModes {
		t.Run(mode.name, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, _ := newLifetimeSchemeFor(t, 120, 0, mode)
			users := userStoreOf(t, scheme)
			pausing := &pausingCASStore{revokeTestStore: users, entered: make(chan struct{}), release: make(chan struct{}), lose: true}

			var recalled, read atomic.Bool
			b := transitionBrowser(t, scheme, "/fanout", func(c *router.Context) error {
				req := c.Request
				recallDone := make(chan struct{})
				go func() {
					defer close(recallDone)
					recalled.Store(scheme.User(req) != nil)
				}()
				<-pausing.entered

				readDone := make(chan struct{})
				go func() {
					defer close(readDone)
					read.Store(scheme.User(req) != nil)
				}()
				waitReturnedOrParked(t, readDone, "resolveAuthenticatedUser")

				commitDone := make(chan struct{})
				go func() {
					defer close(commitDone)
					_ = c.String(http.StatusOK, "done")
				}()
				waitReturnedOrParked(t, commitDone, "commitSession")

				close(pausing.release)
				<-recallDone
				<-readDone
				<-commitDone
				return nil
			})

			b.do(http.MethodPost, "/login")
			b.replayRememberOnly(*b.cookies[rememberCookieName])
			clock.advance(time.Minute)
			scheme.SetUserStore(pausing)
			b.do(http.MethodGet, "/fanout")
			scheme.SetUserStore(users)

			if recalled.Load() {
				t.Error("the recall whose remember-token swap lost reported the user")
			}
			if read.Load() {
				t.Error("a reader during the recall saw the provisional user")
			}
			if b.liveCookie(rememberCookieName) != nil {
				t.Error("a remember cookie was delivered for a recall that lost its swap")
			}
			if b.signedIn() {
				t.Fatal("the session committed during the recall was saved signed in although the recall failed")
			}
		})
	}
}

// A response committed while a recall is between its remember-token swap
// and the queueing of its cookie and undo waits for the recall, so when
// the save then fails the undo runs: the presented remember cookie still
// signs in once saving works again.
func TestRememberRecall_FailedSaveDuringTheTransitionRollsBack(t *testing.T) {
	for _, mode := range lifetimeModes {
		t.Run(mode.name, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, _ := newLifetimeSchemeFor(t, 120, 0, mode)
			users := userStoreOf(t, scheme)
			pausing := &pausingCASStore{revokeTestStore: users, entered: make(chan struct{}), release: make(chan struct{})}

			b := transitionBrowser(t, scheme, "/recall", func(c *router.Context) error {
				req := c.Request
				recallDone := make(chan struct{})
				go func() {
					defer close(recallDone)
					_ = scheme.User(req)
				}()
				<-pausing.entered

				commitDone := make(chan struct{})
				go func() {
					defer close(commitDone)
					_ = c.String(http.StatusOK, "done")
				}()
				waitReturnedOrParked(t, commitDone, "commitSession")

				close(pausing.release)
				<-recallDone
				<-commitDone
				return nil
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
			scheme.SetUserStore(pausing)
			b.do(http.MethodGet, "/recall")
			scheme.SetUserStore(users)
			saveSessionFromMiddleware = orig

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

// flakyUserStore is a revokeTestStore whose user lookups fail while down
// is set: a user database that is briefly unreachable.
type flakyUserStore struct {
	*revokeTestStore
	down atomic.Bool
}

var errUserDatabaseDown = errors.New("user database unreachable")

func (p *flakyUserStore) FindByIDCtx(ctx context.Context, id interface{}) (auth.Authenticatable, error) {
	if p.down.Load() {
		return nil, errUserDatabaseDown
	}
	return p.revokeTestStore.FindByIDCtx(ctx, id)
}

// failingGetRecords is a server session store whose reads fail while down
// is set; deletes still work.
type failingGetRecords struct {
	auth.ServerSessionStore
	down atomic.Bool
}

var errRecordReadFailed = errors.New("session record read failed")

func (s *failingGetRecords) Get(ctx context.Context, id string) (*auth.StoredSession, error) {
	if s.down.Load() {
		return nil, errRecordReadFailed
	}
	return s.ServerSessionStore.Get(ctx, id)
}

// A revocation whose remember credential could not be established as
// cleared (the user lookup or the record read failed) reports it as
// ErrRememberClearPartial, even though the record was deleted: the
// remember credential is still valid, and a remember-only replay signs in
// once the lookup works again. A user or record that is simply absent has
// nothing to clear and succeeds.
func TestRevocation_FailedRememberLookupIsReported(t *testing.T) {
	tests := []struct {
		name   string
		revoke func(mgr *auth.Manager, id string) error
		// userDown fails the user lookup, recordDown the record read.
		userDown, recordDown bool
	}{
		{name: "RevokeSession, user lookup fails", userDown: true,
			revoke: func(mgr *auth.Manager, id string) error { return mgr.RevokeSession(context.Background(), id) }},
		{name: "RevokeSession, record read fails", recordDown: true,
			revoke: func(mgr *auth.Manager, id string) error { return mgr.RevokeSession(context.Background(), id) }},
		{name: "RevokeAllSessions, user lookup fails", userDown: true,
			revoke: func(mgr *auth.Manager, _ string) error { return mgr.RevokeAllSessions(context.Background(), "u1") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, mem := newLifetimeSchemeFor(t, 120, 0, rememberModes[0])
			users := &flakyUserStore{revokeTestStore: userStoreOf(t, scheme)}
			scheme.SetUserStore(users)
			records := &failingGetRecords{ServerSessionStore: mem}
			mgr := auth.NewManager()
			mgr.SetServerSessionStore(records)
			mgr.RegisterScheme("web", scheme)
			b := newRememberBrowser(t, scheme)
			b.do(http.MethodPost, "/login")
			stolen := *b.cookies[rememberCookieName]
			id := onlyRecord(t, mem, "").ID

			users.down.Store(tt.userDown)
			records.down.Store(tt.recordDown)
			err := tt.revoke(mgr, id)
			users.down.Store(false)
			records.down.Store(false)

			if !errors.Is(err, auth.ErrRememberClearPartial) {
				t.Fatalf("revocation with a failed remember lookup returned %v, want auth.ErrRememberClearPartial", err)
			}
			if _, gerr := mem.Get(context.Background(), id); !errors.Is(gerr, auth.ErrSessionNotFound) {
				t.Fatalf("session record after the revocation: %v, want it deleted", gerr)
			}
			// The documented consequence the caller must act on: the
			// remember credential survived.
			clock.advance(time.Minute)
			b.replayRememberOnly(stolen)
			if !b.signedIn() {
				t.Fatal("remember-only replay refused although the clear could not run; the error would then be a false alarm")
			}
		})
	}

	t.Run("absent user", func(t *testing.T) {
		scheme, _ := newLifetimeSchemeFor(t, 120, 0, rememberModes[0])
		if err := scheme.ClearRememberTokensForUser(context.Background(), "ghost"); err != nil {
			t.Fatalf("ClearRememberTokensForUser for a missing user: %v, want nil", err)
		}
	})
	t.Run("absent record", func(t *testing.T) {
		scheme, mem := newLifetimeSchemeFor(t, 120, 0, rememberModes[0])
		mgr := auth.NewManager()
		mgr.SetServerSessionStore(mem)
		mgr.RegisterScheme("web", scheme)
		if err := mgr.RevokeSession(context.Background(), "no-such-session"); err != nil {
			t.Fatalf("RevokeSession of a missing record: %v, want nil", err)
		}
	})
}
