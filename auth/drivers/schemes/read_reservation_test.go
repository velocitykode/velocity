package schemes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// lockedSession is a hookSession whose data is guarded, with a hook on
// Get, so a test can hold a read inside the session without the test
// itself racing on the map.
type lockedSession struct {
	*hookSession
	mu    sync.Mutex
	onGet atomic.Pointer[func()]
}

func (s *lockedSession) Get(key string) interface{} {
	if p := s.onGet.Load(); p != nil {
		(*p)()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[key]
}

func (s *lockedSession) Put(key string, value interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
}

func (s *lockedSession) Remove(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
}

// blockingPutStore is a server session store that runs onPut, when set,
// as a record is written.
type blockingPutStore struct {
	holderRaceStore
	onPut atomic.Pointer[func()]
}

func (s *blockingPutStore) Put(context.Context, *auth.StoredSession) error {
	if p := s.onPut.Load(); p != nil {
		(*p)()
	}
	return nil
}

// A read of the user never returns an identity a concurrent Login wrote
// before that Login finished: the read and the Login take one
// reservation in turn, so either the Login is refused while the read runs,
// or the read runs after the Login; a Login that panics after writing the
// user id leaves nothing a read returns.
func TestSessionScheme_ReadNeverReturnsAnUnpublishedLogin(t *testing.T) {
	s := &lockedSession{hookSession: newHookSession()}
	g, _ := newHookScheme(t, s)
	records := &blockingPutStore{holderRaceStore: holderRaceStore{user: "u2"}}
	g.SetServerSessionStore(records)
	r, w, _ := seamRequest(s)

	readerIn, readerGo := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hold := func() {
		once.Do(func() {
			close(readerIn)
			<-readerGo
		})
	}
	s.onGet.Store(&hold)

	loginIn, loginGo := make(chan struct{}), make(chan struct{})
	panicPut := func() {
		close(loginIn)
		<-loginGo
		panic("login panicked after writing the user id")
	}
	records.onPut.Store(&panicPut)

	readDone := make(chan contract.Authenticatable, 1)
	go func() { readDone <- g.User(r) }()
	<-readerIn

	loginDone := make(chan error, 1)
	go func() {
		defer func() {
			if recover() != nil {
				loginDone <- errors.New("panicked")
			}
		}()
		loginDone <- g.Login(w, r, &revokeTestUser{id: "u2"})
	}()
	var loginErr error
	parked := false
	select {
	case <-loginIn:
		parked = true
	case loginErr = <-loginDone:
	case <-time.After(hostile.Deadline):
		t.Fatal("the Login neither parked in its record write nor returned")
	}

	close(readerGo)
	var got contract.Authenticatable
	hostile.Within(t, hostile.Deadline, func() { got = <-readDone })
	if parked {
		close(loginGo)
		loginErr = <-loginDone
	}
	if got != nil {
		t.Fatalf("the read returned user %v written by a Login in flight (Login: %v)", got.GetAuthIdentifier(), loginErr)
	}
	if !parked && !errors.Is(loginErr, auth.ErrOperationInProgress) {
		t.Errorf("Login while the read ran = %v, want auth.ErrOperationInProgress", loginErr)
	}
}

// A user store that reads the user of the request whose read is looking
// the user up gets auth.ErrOperationInProgress at once, however deep in
// the store's own calls the read is made: the read holds the request's
// reservation and finds the resolve on its own stack, so the call back
// neither recurses nor waits on itself.
func TestSessionScheme_ReadInsideReadIsRefused(t *testing.T) {
	for _, extra := range []int{0, 200} {
		t.Run(fmt.Sprintf("%d frames deeper", extra), func(t *testing.T) {
			g, users, _, r := newReserveRig(t)
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
					return
				}
				atDepth(extra, func() {
					_, err := g.CheckWithError(r)
					once.Do(func() { nestedErr = err })
				})
			}
			users.onFind.Store(&hook)

			hostile.Within(t, hostile.Deadline, func() {
				if u := g.User(r); u == nil {
					t.Error("the outer read did not resolve the signed-in user")
				}
			})
			if got := deepest.Load(); got != 1 {
				t.Errorf("the user store was re-entered to depth %d, want 1", got)
			}
			if !errors.Is(nestedErr, auth.ErrOperationInProgress) {
				t.Errorf("a read from the store during the read = %v, want auth.ErrOperationInProgress", nestedErr)
			}
		})
	}
}

// atDepth calls fn n frames below its caller.
//
//go:noinline
func atDepth(n int, fn func()) {
	if n == 0 {
		fn()
		return
	}
	atDepth(n-1, fn)
}

// Reads on several goroutines of one request resolve the user once: a
// read that arrives while another goroutine's read resolves waits for it
// and returns the same user, and the user store is asked once.
func TestSessionScheme_ConcurrentReadsJoinTheResolver(t *testing.T) {
	g, users, _, r := newReserveRig(t)
	in, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	block := func() {
		if armed.CompareAndSwap(true, false) {
			close(in)
			<-release
		}
	}
	users.onFind.Store(&block)

	first := make(chan contract.Authenticatable, 1)
	go func() { first <- g.User(r) }()
	<-in
	findsBefore := users.finds.Load()

	const siblings = 4
	got := make(chan contract.Authenticatable, siblings)
	for range siblings {
		go func() { got <- g.User(r) }()
	}
	// Evidence that every sibling is waiting on the resolver, not a
	// timer: the holder lists them.
	h := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	hostile.Within(t, hostile.Deadline, func() {
		for {
			h.mu.RLock()
			n := h.waiters
			h.mu.RUnlock()
			if n == siblings {
				return
			}
			select {
			case u := <-got:
				t.Errorf("a sibling read returned (%v) while the first read was still resolving; want it to wait", u)
				return
			default:
				runtime.Gosched()
			}
		}
	})
	close(release)
	want := <-first
	if want == nil {
		t.Fatal("the resolving read returned no user")
	}
	for range siblings {
		if u := <-got; u != want {
			t.Errorf("a sibling read returned %v, want the resolved user %v", u, want)
		}
	}
	if n := users.finds.Load() - findsBefore; n != 0 {
		t.Errorf("sibling reads asked the user store %d more time(s), want none", n)
	}
}

// doneHookCtx is a context whose Done runs hook first.
type doneHookCtx struct {
	context.Context
	hook func()
}

func (c doneHookCtx) Done() <-chan struct{} {
	c.hook()
	return c.Context.Done()
}

// A sibling read waits with its request's context: a Done that calls back
// into the scheme on the waiting goroutine is re-entry and is refused at
// once, and a context that ends releases the waiter with
// auth.ErrOperationInProgress while the resolver finishes.
func TestSessionScheme_WaitingReadHonoursItsContext(t *testing.T) {
	g, users, _, r := newReserveRig(t)
	in, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	block := func() {
		if armed.CompareAndSwap(true, false) {
			close(in)
			<-release
		}
	}
	users.onFind.Store(&block)
	first := make(chan contract.Authenticatable, 1)
	go func() { first <- g.User(r) }()
	<-in

	cctx, cancel := context.WithCancel(r.Context())
	var nested atomic.Pointer[error]
	var inDone sync.Once
	sibling := r.WithContext(doneHookCtx{Context: cctx, hook: func() {
		inDone.Do(func() {
			_, err := g.CheckWithError(r)
			nested.Store(&err)
			cancel()
		})
	}})
	var siblingErr error
	hostile.Within(t, hostile.Deadline, func() { _, siblingErr = g.CheckWithError(sibling) })
	if p := nested.Load(); p == nil || !errors.Is(*p, auth.ErrOperationInProgress) {
		t.Errorf("a read from the waiting read's context = %v, want auth.ErrOperationInProgress", p)
	}
	if !errors.Is(siblingErr, auth.ErrOperationInProgress) {
		t.Errorf("a waiting read whose context ended = %v, want auth.ErrOperationInProgress", siblingErr)
	}
	close(release)
	if u := <-first; u == nil {
		t.Error("the resolver did not finish after its waiter left")
	}
}

// An operation that may change the session clears the published identity:
// after a Login of another user, the next read returns that user.
func TestSessionScheme_LoginClearsThePublishedIdentity(t *testing.T) {
	g, _, w, r := newReserveRig(t)
	if u := g.User(r); u == nil || u.GetAuthIdentifier() != "u1" {
		t.Fatalf("premise: signed in as u1, got %v", u)
	}
	if err := g.Login(w, r, &revokeTestUser{id: "u2"}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if u := g.User(r); u == nil || u.GetAuthIdentifier() != "u2" {
		t.Fatalf("after Login of u2 the read returned %v, want u2", u)
	}
	if err := g.Logout(w, r); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if u := g.User(r); u != nil {
		t.Fatalf("after Logout the read returned %v, want none", u.GetAuthIdentifier())
	}
}

// ResolveSession runs under the request's reservation: while an operation
// that may change the session holds it, ResolveSession fails closed
// without reading the session or the stores.
func TestSessionScheme_ResolveSessionRefusedWhileAnOperationRuns(t *testing.T) {
	g, users, w, r := newReserveRig(t)
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
	done := make(chan error, 1)
	go func() { done <- g.Logout(w, r) }()
	<-entered
	sess, err := g.ResolveSession(r)
	close(release)
	<-done
	if !errors.Is(err, auth.ErrOperationInProgress) || sess != nil {
		t.Errorf("ResolveSession while a Logout runs = (%v, %v), want (nil, auth.ErrOperationInProgress)", sess, err)
	}
}

// A standalone Login (outside the session middleware) on a request that
// carries a WithSessionContext holder reserves that holder: a store that
// calls Login for the same request from the Login's save is refused, and
// the session is saved once.
func TestSessionScheme_StandaloneLoginReservesTheRequestHolder(t *testing.T) {
	s := newHookSession()
	g, _ := newHookScheme(t, s)
	r := WithSessionContext(httptest.NewRequest(http.MethodPost, "/", nil))
	w := httptest.NewRecorder()

	var (
		first     atomic.Bool
		nestedErr error
		readErr   error
	)
	store := g.store.(*hookSessionStore)
	store.s = &saveHookSession{hookSession: s, onSave: func() {
		if first.CompareAndSwap(false, true) {
			nestedErr = g.Login(w, r, &revokeTestUser{id: "u2"})
			_, readErr = g.CheckWithError(r)
		}
	}}

	if err := g.Login(w, r, &revokeTestUser{id: "u1"}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !first.Load() {
		t.Fatal("premise: the Login saved no session")
	}
	if !errors.Is(nestedErr, auth.ErrOperationInProgress) {
		t.Errorf("Login from the save of a standalone Login = %v, want auth.ErrOperationInProgress", nestedErr)
	}
	if !errors.Is(readErr, auth.ErrOperationInProgress) {
		t.Errorf("a read of the request from the save of a standalone Login = %v, want auth.ErrOperationInProgress", readErr)
	}
	if n := s.saves.Load(); n != 1 {
		t.Errorf("the session was saved %d time(s), want 1", n)
	}
}

// saveHookSession is a hookSession that runs onSave as it is saved.
type saveHookSession struct {
	*hookSession
	onSave func()
}

func (s *saveHookSession) Save(w http.ResponseWriter) error {
	s.onSave()
	return s.hookSession.Save(w)
}

// Readers on many goroutines of one request race Logins and Logouts of
// the same request: every read returns a whole identity (a user the
// request was signed in as, or none) and nothing races.
func TestSessionScheme_ReadsRaceOperations(t *testing.T) {
	s := &lockedSession{hookSession: newHookSession()}
	s.data[auth.UserIDSessionKey] = "u1"
	g, _ := newHookScheme(t, s)
	r, w, _ := seamRequest(s)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				u, _ := g.CheckWithError(r)
				_ = u
				if user := g.User(r); user != nil {
					if id := user.GetAuthIdentifier(); id != "u1" && id != "u2" {
						t.Errorf("read returned unknown user %v", id)
					}
				}
			}
		}()
	}
	for i := range 200 {
		id := "u1"
		if i%2 == 1 {
			id = "u2"
		}
		_ = g.Login(w, r, &revokeTestUser{id: id})
		_ = g.Logout(w, r)
	}
	close(stop)
	wg.Wait()
}

// A standalone Login that panics after it began changing the session tears
// the request holder it reserved as its anchor: reads of the request then
// read signed out instead of the half-changed session.
func TestSessionScheme_StandaloneLoginPanicTearsTheRequestHolder(t *testing.T) {
	s := newHookSession()
	s.data[auth.UserIDSessionKey] = "u1"
	g, _ := newHookScheme(t, s)
	r := WithSessionContext(httptest.NewRequest(http.MethodPost, "/", nil))
	r.Context().Value(sessionCtxKey{}).(*sessionHolder).setSession(s)
	w := httptest.NewRecorder()
	if u := g.User(r); u == nil {
		t.Fatal("premise: the request is not signed in")
	}
	s.onRegenerate = func(s *hookSession) { s.id = "half-regenerated"; panic("regenerate") }
	mustPanic(t, func() { _ = g.Login(w, r, &revokeTestUser{id: "u2"}) })
	s.onRegenerate = nil
	if u := g.User(r); u != nil {
		t.Errorf("a read after the torn standalone Login returned %v, want signed out", u.GetAuthIdentifier())
	}
}
