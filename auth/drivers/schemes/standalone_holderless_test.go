package schemes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/contract"
)

// reenteringRecords calls back into the scheme for the request it is
// serving, once, from inside the sign-in's record write.
type reenteringRecords struct {
	auth.ServerSessionStore
	reenter func() error
	done    bool
	err     error
	puts    int
}

func (s *reenteringRecords) Put(ctx context.Context, rec *auth.StoredSession) error {
	s.puts++
	if !s.done {
		s.done = true
		s.err = s.reenter()
	}
	return s.ServerSessionStore.Put(ctx, rec)
}

func sessionCookieLines(w *httptest.ResponseRecorder, name string) int {
	n := 0
	for _, line := range w.Header().Values("Set-Cookie") {
		if strings.HasPrefix(line, name+"=") {
			n++
		}
	}
	return n
}

func reenteringScheme(t *testing.T) (*SessionScheme, *reenteringRecords, *revokeTestUser) {
	t.Helper()
	mem := session.NewMemoryStore()
	t.Cleanup(func() { _ = mem.Close(context.Background()) })
	scheme, _ := newRevokeScheme(t, nil)
	records := &reenteringRecords{ServerSessionStore: mem}
	scheme.SetServerSessionStore(records)
	return scheme, records, &revokeTestUser{id: "u1"}
}

// A request with no session holder (neither the session middleware nor
// WithSessionContext) gives the scheme nothing to reserve, so a store that
// calls back into the scheme for the same request could not be told from a
// first call: both ran and both wrote a session cookie. The four operations
// refuse such a request before any side effect.
func TestSessionScheme_OperationsRefuseARequestWithNoSessionHolder(t *testing.T) {
	ops := map[string]func(g *SessionScheme, w http.ResponseWriter, r *http.Request, u contract.Authenticatable) error{
		"Login": func(g *SessionScheme, w http.ResponseWriter, r *http.Request, u contract.Authenticatable) error {
			return g.Login(w, r, u)
		},
		"LoginByID": func(g *SessionScheme, w http.ResponseWriter, r *http.Request, u contract.Authenticatable) error {
			return g.LoginByID(w, r, "u1")
		},
		"Attempt": func(g *SessionScheme, w http.ResponseWriter, r *http.Request, u contract.Authenticatable) error {
			_, err := g.Attempt(w, r, map[string]interface{}{"email": "u1", "password": "x"})
			return err
		},
		"Logout": func(g *SessionScheme, w http.ResponseWriter, r *http.Request, u contract.Authenticatable) error {
			return g.Logout(w, r)
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			scheme, records, user := reenteringScheme(t)
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			records.reenter = func() error { return scheme.Login(w, r, user) }

			err := op(scheme, w, r, user)
			if !errors.Is(err, auth.ErrNoSessionContext) {
				t.Errorf("%s on a request with no session holder = %v, want auth.ErrNoSessionContext", name, err)
			}
			if got := w.Header().Values("Set-Cookie"); len(got) != 0 {
				t.Errorf("%s wrote cookies before it was refused: %v", name, got)
			}
			if records.puts != 0 {
				t.Errorf("%s reached the record store %d times before it was refused", name, records.puts)
			}
		})
	}
}

// With WithSessionContext the request carries a holder: the operation
// runs, and the store's call back into the scheme for the same request is
// refused, so the response carries one session cookie.
func TestSessionScheme_ReentryOnAWrappedRequestIsRefused(t *testing.T) {
	scheme, records, user := reenteringScheme(t)
	w := httptest.NewRecorder()
	r := WithSessionContext(httptest.NewRequest(http.MethodPost, "/", nil))
	records.reenter = func() error { return scheme.Login(w, r, user) }

	if err := scheme.Login(w, r, user); err != nil {
		t.Fatalf("Login on a wrapped request: %v", err)
	}
	if !records.done {
		t.Fatal("premise: the sign-in did not reach the record store")
	}
	if !errors.Is(records.err, auth.ErrOperationInProgress) {
		t.Fatalf("the store's Login for the same request = %v, want auth.ErrOperationInProgress", records.err)
	}
	if n := sessionCookieLines(w, scheme.config.Name); n != 1 {
		t.Fatalf("the response carries %d session cookies, want 1: %v", n, w.Header().Values("Set-Cookie"))
	}
}

// What the refusal closes: on a bare request the store's call back ran as
// a sign-in of its own and the response carried two session cookies.
func TestSessionScheme_HolderlessLoginWithAReenteringStoreWritesNoCookie(t *testing.T) {
	scheme, records, user := reenteringScheme(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	records.reenter = func() error { return scheme.Login(w, r, user) }

	err := scheme.Login(w, r, user)
	if n := sessionCookieLines(w, scheme.config.Name); n != 0 {
		t.Fatalf("a sign-in on a request with no session holder wrote %d session cookies (err %v, store call back %v), want none", n, err, records.err)
	}
	if !errors.Is(err, auth.ErrNoSessionContext) {
		t.Fatalf("Login = %v, want auth.ErrNoSessionContext", err)
	}
}

// A Login handed no user is refused for the missing session context like
// any other: the request is reserved before the user is looked at, so a
// holder-less caller gets the same answer whatever it passed.
func TestSessionScheme_LoginWithNoUserOnARequestWithNoSessionHolder(t *testing.T) {
	users := map[string]contract.Authenticatable{
		"nil interface": nil,
		"typed nil":     (*revokeTestUser)(nil),
	}
	for name, user := range users {
		t.Run(name, func(t *testing.T) {
			scheme, records, _ := reenteringScheme(t)
			w := httptest.NewRecorder()
			err := scheme.Login(w, httptest.NewRequest(http.MethodPost, "/", nil), user)
			if !errors.Is(err, auth.ErrNoSessionContext) {
				t.Errorf("Login(no user) on a request with no session holder = %v, want auth.ErrNoSessionContext", err)
			}
			if got := w.Header().Values("Set-Cookie"); len(got) != 0 || records.puts != 0 {
				t.Errorf("the refused Login wrote cookies %v and reached the record store %d times", got, records.puts)
			}
		})
	}
}

// On a request that carries a session context a Login handed no user is
// still refused with auth.ErrUserNotFound, changes nothing, and leaves the
// request free for the next operation.
func TestSessionScheme_LoginWithNoUserOnAWrappedRequest(t *testing.T) {
	scheme, records, user := reenteringScheme(t)
	records.done = true
	w := httptest.NewRecorder()
	r := WithSessionContext(httptest.NewRequest(http.MethodPost, "/", nil))
	if err := scheme.Login(w, r, nil); !errors.Is(err, auth.ErrUserNotFound) {
		t.Fatalf("Login(nil) = %v, want auth.ErrUserNotFound", err)
	}
	if got := w.Header().Values("Set-Cookie"); len(got) != 0 || records.puts != 0 {
		t.Fatalf("Login(nil) wrote cookies %v and reached the record store %d times", got, records.puts)
	}
	if err := scheme.Login(w, r, user); err != nil {
		t.Fatalf("the Login after it, on the same request: %v", err)
	}
}

// The known limit of the reads on a request with no session context: a
// valid remember cookie starts a recall there, which is refused only at
// its last step (there is nowhere to deliver the rotated credential). By
// then it has rotated the CSRF token and written a server record for a
// session id no client receives. The read answers signed out.
func TestSessionScheme_ReadWithARememberCookieAndNoSessionHolderLeavesRecallSideEffects(t *testing.T) {
	for _, name := range []string{"User", "Check"} {
		t.Run(name, func(t *testing.T) {
			scheme, records, _ := reenteringScheme(t)
			records.done = true
			rotator := &fakeCSRFRotator{}
			scheme.SetUserStore(&rememberRevivalStore{user: &revokeTestUser{id: "u1"}})
			scheme.SetCSRFTokenRotator(rotator)
			cookie := mintRememberCookie(t, scheme)
			rotations, puts := len(rotator.rotated), records.puts

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(cookie)
			signedIn := false
			if name == "User" {
				signedIn = scheme.User(req) != nil
			} else {
				signedIn = scheme.Check(req)
			}
			if signedIn {
				t.Fatal("the read on a request with no session context answered signed in")
			}
			if got := len(rotator.rotated) - rotations; got != 1 {
				t.Errorf("the refused recall rotated the CSRF token %d times, want 1", got)
			}
			if got := records.puts - puts; got != 1 {
				t.Errorf("the refused recall wrote %d server records, want 1", got)
			}
		})
	}
}
