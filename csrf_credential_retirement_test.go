package velocity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/internal/sessionclock"
	"github.com/velocitykode/velocity/router"
)

// retirementInstance is csrfBagInstance with a user store that finds the
// test user and a /whoami route that answers 200 only for a request the
// session scheme signs in.
func retirementInstance(t *testing.T, sessionStore string, records auth.ServerSessionStore, singleUse bool) *App {
	t.Helper()
	a := csrfBagInstance(t, sessionStore, records, singleUse)
	scheme := retirementScheme(t, a)
	scheme.SetUserStore(&saveSeamUserStore{user: &saveSeamUser{id: 9}})
	a.Router.Get("/whoami", func(c *router.Context) error {
		if ok, _ := scheme.CheckWithError(c.Request); !ok {
			return c.String(http.StatusUnauthorized, "guest")
		}
		return c.String(http.StatusOK, "signed in")
	})
	return a
}

func retirementScheme(t *testing.T, a *App) *schemes.SessionScheme {
	t.Helper()
	sc, err := auth.FromServices(a.Services).DefaultScheme()
	if err != nil {
		t.Fatalf("DefaultScheme: %v", err)
	}
	return sc.(*schemes.SessionScheme)
}

func (j csrfBagJar) clone() csrfBagJar {
	c := csrfBagJar{}
	for k, v := range j {
		c[k] = v
	}
	return c
}

// TestLogin_RetiresThePreviousSessionAndItsToken pins that a sign-in
// retires the session id it rotates away from, with the CSRF token that
// session carried: a captured copy of the pre-login session cookie and its
// token is refused, and so is a captured copy of an earlier signed-in
// session after the visitor signs in again, for authentication and for
// CSRF alike. Rows cover the cookie store alone, the cookie store with the
// shared revocation index, and the server store.
func TestLogin_RetiresThePreviousSessionAndItsToken(t *testing.T) {
	for _, tt := range []struct {
		name   string
		store  string
		shared bool
	}{
		{"cookie session store", "cookie", false},
		{"cookie session store with a revocation index", "cookie", true},
		{"server session store", "server", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var records auth.ServerSessionStore
			if tt.shared {
				records = session.NewMemoryStore()
			}
			a := retirementInstance(t, tt.store, records, false)
			name := a.config.Session.Name

			jar := csrfBagJar{}
			jar.send(t, a.Router, http.MethodGet, "/form", "")
			guest := jar.clone()
			guestToken := jar.xsrf(t)

			if w := jar.send(t, a.Router, http.MethodPost, "/login", guestToken); w.Code != http.StatusOK {
				t.Fatalf("POST /login = %d", w.Code)
			}
			if jar[name] == guest[name] {
				t.Fatal("premise: login did not issue a new session cookie")
			}
			if w := guest.clone().send(t, a.Router, http.MethodPost, "/form", guestToken); w.Code != contract419 {
				t.Fatalf("POST replaying the pre-login session cookie and its token = %d, want 419", w.Code)
			}

			first := jar.clone()
			firstToken := jar.xsrf(t)
			if w := first.clone().send(t, a.Router, http.MethodGet, "/whoami", ""); w.Code != http.StatusOK {
				t.Fatalf("premise: signed-in session = %d, want 200", w.Code)
			}
			if w := jar.send(t, a.Router, http.MethodPost, "/login", firstToken); w.Code != http.StatusOK {
				t.Fatalf("second POST /login = %d", w.Code)
			}
			if w := first.clone().send(t, a.Router, http.MethodPost, "/form", firstToken); w.Code != contract419 {
				t.Fatalf("POST replaying the earlier signed-in session and its token = %d, want 419", w.Code)
			}
			if w := first.clone().send(t, a.Router, http.MethodGet, "/whoami", ""); w.Code != http.StatusUnauthorized {
				t.Fatalf("GET replaying the earlier signed-in session = %d, want 401", w.Code)
			}
			if w := jar.send(t, a.Router, http.MethodPost, "/form", jar.xsrf(t)); w.Code != http.StatusOK {
				t.Fatalf("control: POST with the current session and token = %d, want 200", w.Code)
			}
		})
	}
}

// failingDeleteRecords is a record store whose Delete fails, standing in
// for a revocation index that is down.
type failingDeleteRecords struct {
	auth.ServerSessionStore
}

func (failingDeleteRecords) Delete(context.Context, string) error {
	return errors.New("records down")
}

// TestLogin_FailsClosedWhenThePreviousSessionCannotBeRetired pins that a
// sign-in whose previous session cannot be retired from the revocation
// index does not sign the visitor in: the old session would otherwise stay
// usable next to the new one.
func TestLogin_FailsClosedWhenThePreviousSessionCannotBeRetired(t *testing.T) {
	a := retirementInstance(t, "cookie", failingDeleteRecords{session.NewMemoryStore()}, false)
	jar := csrfBagJar{}
	jar.send(t, a.Router, http.MethodGet, "/form", "")
	if w := jar.send(t, a.Router, http.MethodPost, "/login", jar.xsrf(t)); w.Code == http.StatusOK {
		t.Fatalf("POST /login with an index that cannot retire the old session = %d, want a failure", w.Code)
	}
	if w := jar.send(t, a.Router, http.MethodGet, "/whoami", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("GET after the failed login = %d, want 401", w.Code)
	}
}

// TestCSRFToken_ConsumedTokenNeverReturnsThroughRenewal pins single use
// against captured cookies that the activity seam keeps renewing. After the
// token is consumed, two captured copies of the cookie that still carried
// it are renewed by safe requests an hour later: one on a page that never
// reads the token (the renewed cookie still carries it), one on a page that
// reads it (the session gives up the consumed token and gets a fresh one).
// Past the idle window measured from the consumption, the consumed token
// stays refused on both, and the fresh token works.
func TestCSRFToken_ConsumedTokenNeverReturnsThroughRenewal(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	restore := sessionclock.Set(func() time.Time { return now })
	t.Cleanup(restore)

	a := retirementInstance(t, "cookie", nil, true)
	name := a.config.Session.Name
	jar := csrfBagJar{}
	jar.send(t, a.Router, http.MethodGet, "/form", "")
	tok := jar.send(t, a.Router, http.MethodGet, "/token", "").Body.String()
	if tok == "" {
		t.Fatal("the page carries no CSRF token")
	}
	unread := jar.clone()
	read := jar.clone()
	if w := jar.send(t, a.Router, http.MethodPost, "/form", tok); w.Code != http.StatusOK {
		t.Fatalf("first POST = %d, want 200", w.Code)
	}

	now = now.Add(60 * time.Minute)
	for _, renewal := range []struct {
		jar  csrfBagJar
		path string
	}{{unread, "/form"}, {read, "/token"}} {
		before := renewal.jar[name]
		renewal.jar.send(t, a.Router, http.MethodGet, renewal.path, "")
		if renewal.jar[name] == before {
			t.Fatalf("premise: GET %s did not renew the captured cookie", renewal.path)
		}
	}

	now = now.Add(61 * time.Minute)
	if w := unread.clone().send(t, a.Router, http.MethodPost, "/form", tok); w.Code != contract419 {
		t.Fatalf("POST with the consumed token on a renewed captured cookie past the idle window = %d, want 419", w.Code)
	}
	if w := read.clone().send(t, a.Router, http.MethodPost, "/form", tok); w.Code != contract419 {
		t.Fatalf("POST with the consumed token on the captured cookie renewed by a token read = %d, want 419", w.Code)
	}
	fresh := read.send(t, a.Router, http.MethodGet, "/token", "").Body.String()
	if w := read.send(t, a.Router, http.MethodPost, "/form", fresh); w.Code != http.StatusOK {
		t.Fatalf("POST with the token the renewed session gave the page = %d, want 200 (the session handed back the consumed token)", w.Code)
	}
}

// TestCSRFToken_RefusedAfterLogoutOnAnotherInstance pins that the CSRF
// check refuses a session the shared revocation index rejects: signed in
// and signed out on instance A, the captured session cookie and its token
// are replayed on instance B, which shares the key and the index but not
// A's in-process revocation list.
func TestCSRFToken_RefusedAfterLogoutOnAnotherInstance(t *testing.T) {
	records := session.NewMemoryStore()
	instA := retirementInstance(t, "cookie", records, false)
	instB := retirementInstance(t, "cookie", records, false)

	jar := csrfBagJar{}
	jar.send(t, instA.Router, http.MethodGet, "/form", "")
	if w := jar.send(t, instA.Router, http.MethodPost, "/login", jar.xsrf(t)); w.Code != http.StatusOK {
		t.Fatalf("POST /login on A = %d", w.Code)
	}
	captured := jar.clone()
	tok := jar.xsrf(t)
	if w := captured.clone().send(t, instB.Router, http.MethodPost, "/form", tok); w.Code != http.StatusOK {
		t.Fatalf("control: POST on B before logout = %d, want 200", w.Code)
	}
	if w := jar.send(t, instA.Router, http.MethodPost, "/logout", tok); w.Code != http.StatusOK {
		t.Fatalf("POST /logout on A = %d", w.Code)
	}
	if w := captured.clone().send(t, instB.Router, http.MethodPost, "/form", tok); w.Code != contract419 {
		t.Fatalf("POST on B replaying the session signed out on A = %d, want 419", w.Code)
	}
}

// TestCSRFMiddleware_OutsideTheSaveSeamFailsClosed pins that the session
// CSRF token store answers only inside a scope that saves the session: a
// plain net/http stack that attaches the session cache with
// schemes.WithSessionContext but mounts the CSRF middleware outside the
// session middleware issues no token and accepts none, even for a valid
// session cookie and token.
func TestCSRFMiddleware_OutsideTheSaveSeamFailsClosed(t *testing.T) {
	a := retirementInstance(t, "cookie", nil, false)
	jar := csrfBagJar{}
	jar.send(t, a.Router, http.MethodGet, "/form", "")
	tok := jar.xsrf(t)
	if w := jar.clone().send(t, a.Router, http.MethodPost, "/form", tok); w.Code != http.StatusOK {
		t.Fatalf("control: POST on the app router = %d, want 200", w.Code)
	}

	protect := a.Services.CSRF.(*csrf.CSRF)
	plain := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protect.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(w, schemes.WithSessionContext(r))
	})

	w := jar.clone().send(t, plain, http.MethodPost, "/form", tok)
	if w.Code != contract419 {
		t.Fatalf("POST through a stack with no save seam = %d, want 419", w.Code)
	}
	get := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/form", nil)
	for k, v := range jar {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	plain.ServeHTTP(get, r)
	for _, c := range get.Result().Cookies() {
		if c.Name == "XSRF-TOKEN" {
			t.Fatal("GET through a stack with no save seam wrote XSRF-TOKEN")
		}
	}
}
