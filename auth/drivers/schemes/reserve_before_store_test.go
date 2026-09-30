package schemes

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// hookUsers is a revokeTestStore that runs onFind, when set, before a user
// lookup by id, and counts every lookup by id and by credentials.
type hookUsers struct {
	*revokeTestStore
	onFind       atomic.Pointer[func()]
	finds        atomic.Int32
	credentialed atomic.Int32
}

func (u *hookUsers) FindByIDCtx(ctx context.Context, id interface{}) (auth.Authenticatable, error) {
	u.finds.Add(1)
	if p := u.onFind.Load(); p != nil {
		(*p)()
	}
	return u.revokeTestStore.FindByIDCtx(ctx, id)
}

func (u *hookUsers) FindByCredentialsCtx(ctx context.Context, _ map[string]interface{}) (auth.Authenticatable, error) {
	u.credentialed.Add(1)
	return u.revokeTestStore.FindByIDCtx(ctx, "u1")
}

// checkCountingThrottler counts the throttle checks an attempt makes.
type checkCountingThrottler struct{ checks atomic.Int32 }

func (c *checkCountingThrottler) Allow(*http.Request, string) bool {
	c.checks.Add(1)
	return true
}
func (c *checkCountingThrottler) RecordFailure(*http.Request, string) {}
func (c *checkCountingThrottler) RecordSuccess(*http.Request, string) {}

// deleteHookServerStore vouches for every record and runs onDelete, when
// set, as a record is deleted.
type deleteHookServerStore struct {
	holderRaceStore
	onDelete func()
}

func (s *deleteHookServerStore) Delete(context.Context, string) error {
	if s.onDelete != nil {
		s.onDelete()
	}
	return nil
}

// newReserveRig returns a scheme over a signed-in (u1) custom session
// served inside the save seam, with a hooked user store.
func newReserveRig(t *testing.T) (*SessionScheme, *hookUsers, http.ResponseWriter, *http.Request) {
	t.Helper()
	s := newHookSession()
	s.data[auth.UserIDSessionKey] = "u1"
	g, base := newHookScheme(t, s)
	users := &hookUsers{revokeTestStore: base}
	g.SetUserStore(users)
	r, w, _ := seamRequest(s)
	return g, users, w, r
}

// A user store that calls LoginByID for the request whose Logout is
// looking the user up gets auth.ErrOperationInProgress at once: LoginByID
// takes the request's reservation before it calls the store, so the call
// back does not reach the store again and recurse.
func TestSessionScheme_StoreCallingLoginByIDDuringLogoutIsRefusedBeforeTheStore(t *testing.T) {
	g, users, w, r := newReserveRig(t)
	var (
		depth, deepest atomic.Int32
		once           sync.Once
		nestedErr      error
	)
	hook := func() {
		d := depth.Add(1)
		defer depth.Add(-1)
		if d > deepest.Load() {
			deepest.Store(d)
		}
		if d > 3 {
			return // bound the recursion the defect causes
		}
		err := g.LoginByID(w, r, "u2")
		once.Do(func() { nestedErr = err })
	}
	users.onFind.Store(&hook)

	_ = g.Logout(w, r)

	if got := deepest.Load(); got != 1 {
		t.Errorf("the user store was re-entered to depth %d, want 1 (LoginByID called the store before reserving)", got)
	}
	if !errors.Is(nestedErr, auth.ErrOperationInProgress) {
		t.Errorf("LoginByID from the store during Logout returned %v, want auth.ErrOperationInProgress", nestedErr)
	}
}

// While an operation of the request holds its reservation, LoginByID and
// Attempt on another goroutine of the request are refused with
// auth.ErrOperationInProgress before any store, throttle or credential
// work.
func TestSessionScheme_BusyLoginByIDAndAttemptDoNoStoreOrCredentialWork(t *testing.T) {
	g, users, w, r := newReserveRig(t)
	throttler := &checkCountingThrottler{}
	g.SetLoginThrottler(throttler)
	g.SetAttemptFloor(-1)

	entered, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	block := func() {
		if armed.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
	}
	users.onFind.Store(&block)
	done := make(chan error)
	go func() { done <- g.Logout(w, r) }()
	<-entered
	defer func() {
		close(release)
		<-done
	}()

	findsBefore := users.finds.Load()
	if err := g.LoginByID(w, r, "u2"); !errors.Is(err, auth.ErrOperationInProgress) {
		t.Errorf("busy LoginByID returned %v, want auth.ErrOperationInProgress", err)
	}
	if n := users.finds.Load() - findsBefore; n != 0 {
		t.Errorf("busy LoginByID looked the user up %d time(s), want none", n)
	}

	ok, err := g.Attempt(w, r, map[string]interface{}{"email": "u1@example.com", "password": "pw"})
	if ok || !errors.Is(err, auth.ErrOperationInProgress) {
		t.Errorf("busy Attempt returned (%v, %v), want (false, auth.ErrOperationInProgress)", ok, err)
	}
	if n := throttler.checks.Load(); n != 0 {
		t.Errorf("busy Attempt made %d throttle check(s), want none", n)
	}
	if n := users.credentialed.Load(); n != 0 {
		t.Errorf("busy Attempt looked credentials up %d time(s), want none", n)
	}
}

// Every store call a Logout makes runs under the request's reservation,
// the server-side teardown included: a server session store that signs
// the request in from its Delete gets auth.ErrOperationInProgress.
func TestSessionScheme_LogoutTeardownStoreCallbackIsRefused(t *testing.T) {
	g, _, w, r := newReserveRig(t)
	var (
		first     atomic.Bool
		called    bool
		nestedErr error
	)
	store := &deleteHookServerStore{holderRaceStore: holderRaceStore{user: "u1"}}
	store.onDelete = func() {
		// First Delete only; a Login that got through deletes again.
		if first.CompareAndSwap(false, true) {
			called = true
			nestedErr = g.Login(w, r, &revokeTestUser{id: "u2"})
		}
	}
	g.SetServerSessionStore(store)

	if err := g.Logout(w, r); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !called {
		t.Fatal("premise: Logout deleted no server record")
	}
	if !errors.Is(nestedErr, auth.ErrOperationInProgress) {
		t.Errorf("Login from the server store's Delete during Logout returned %v, want auth.ErrOperationInProgress", nestedErr)
	}
}
