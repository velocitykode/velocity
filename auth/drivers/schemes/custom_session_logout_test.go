package schemes

import (
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/router"
)

// plainSession is a custom auth.Session with neither IsModified nor
// IsDestroyed: nothing the scheme can ask tells it the session was
// invalidated. A second Invalidate fails.
type plainSession struct {
	mockSession
	mu          sync.Mutex
	invalidates int
}

func (s *plainSession) Invalidate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidates++
	s.data = map[string]interface{}{}
	if s.invalidates > 1 {
		return errors.New("plain session invalidated twice")
	}
	return nil
}

// Save sets the session cookie, or deletes it once the session was
// invalidated, as a cookie-backed custom session does.
func (s *plainSession) Save(w http.ResponseWriter) error {
	if s.count() > 0 {
		http.SetCookie(w, &http.Cookie{Name: "vel_session", Value: "", Path: "/", MaxAge: -1})
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: "vel_session", Value: s.id, Path: "/"})
	return nil
}

func (s *plainSession) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.invalidates
}

// plainStore serves one plainSession per id.
type plainStore struct {
	mu       sync.Mutex
	sessions map[string]*plainSession
	next     int
}

func (s *plainStore) Create(string) (auth.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	ps := &plainSession{mockSession: *newMockSession()}
	ps.id = "plain-" + string(rune('0'+s.next))
	s.sessions[ps.id] = ps
	return ps, nil
}

func (s *plainStore) Get(r *http.Request, _ string) (auth.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, err := r.Cookie("vel_session"); err == nil {
		if ps, ok := s.sessions[c.Value]; ok {
			return ps, nil
		}
	}
	return nil, errors.New("no session")
}

func (s *plainStore) Save(w http.ResponseWriter, session auth.Session) error {
	return session.Save(w)
}

func (s *plainStore) Destroy(string) error               { return nil }
func (s *plainStore) GarbageCollect(time.Duration) error { return nil }

// A Logout ends a custom session that cannot report it was invalidated
// exactly once: the commit knows the session ended from the Logout itself,
// so it does not invalidate it again when the session's save deletes the
// cookie, and writes no warning. A Login after the Logout in the same
// request signs in on a fresh session, which the commit saves.
func TestSessionScheme_LogoutEndsACustomSessionOnce(t *testing.T) {
	for _, loginAfter := range []bool{false, true} {
		name := "logout"
		if loginAfter {
			name = "logout then login"
		}
		t.Run(name, func(t *testing.T) {
			store := &plainStore{sessions: map[string]*plainSession{}}
			signedIn, _ := store.Create("")
			signedIn.Put(auth.UserIDSessionKey, "1")
			scheme := &SessionScheme{
				store:  store,
				config: auth.SessionConfig{Name: "vel_session"},
				hasher: auth.NewBcryptHasher(4),
			}
			scheme.userStore.Store(&userStoreHolder{p: &mockUserStore{}})
			scheme.throttler.Store(&throttlerHolder{t: auth.NoopLoginThrottler{}})
			out := fallbacklogtest.Capture(t)

			var logoutErr, loginErr error
			r := router.New()
			r.Use(scheme.SessionMiddleware())
			r.Post("/logout", func(c *router.Context) error {
				logoutErr = scheme.Logout(c.Response, c.Request)
				if loginAfter {
					loginErr = scheme.Login(c.Response, c.Request, &auth.AuthUser{ID: "1"})
				}
				return c.String(http.StatusOK, "done")
			})
			req, _ := http.NewRequest(http.MethodPost, "/logout", nil)
			req.AddCookie(&http.Cookie{Name: "vel_session", Value: signedIn.ID()})
			w := &headerRecorder{header: http.Header{}}
			r.ServeHTTP(w, req)

			if logoutErr != nil || loginErr != nil {
				t.Fatalf("Logout = %v, Login = %v", logoutErr, loginErr)
			}
			if n := signedIn.(*plainSession).count(); n != 1 {
				t.Errorf("Invalidate calls = %d, want 1", n)
			}
			if s := out.String(); s != "" {
				t.Errorf("warnings written: %s", s)
			}
			resp := http.Response{Header: w.header}
			var lines []*http.Cookie
			for _, c := range resp.Cookies() {
				if c.Name == "vel_session" {
					lines = append(lines, c)
				}
			}
			switch {
			case len(lines) != 1:
				t.Errorf("session cookie lines = %+v, want one", lines)
			case loginAfter && (lines[0].MaxAge < 0 || lines[0].Value == signedIn.ID() || lines[0].Value == ""):
				t.Errorf("session cookie = %+v, want the fresh session's id", lines[0])
			case !loginAfter && lines[0].MaxAge >= 0:
				t.Errorf("session cookie = %+v, want its deletion", lines[0])
			}
		})
	}
}

// headerRecorder is a minimal ResponseWriter.
type headerRecorder struct {
	header http.Header
	code   int
}

func (h *headerRecorder) Header() http.Header         { return h.header }
func (h *headerRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (h *headerRecorder) WriteHeader(code int)        { h.code = code }
