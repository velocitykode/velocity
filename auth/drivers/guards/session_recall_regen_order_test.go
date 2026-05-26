package guards

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
)

// orderTrackingSession records every Regenerate / Put("user_id") call so
// the test can assert Regenerate happened strictly before user_id was
// anchored. Mirrors *auth.BaseSession's contract; only the bookkeeping is
// new.
type orderTrackingSession struct {
	*auth.BaseSession

	mu    sync.Mutex
	calls []string // "regen" or "put-user_id"
}

func newOrderTrackingSession(id string) *orderTrackingSession {
	return &orderTrackingSession{BaseSession: auth.NewSession(id)}
}

func (s *orderTrackingSession) Regenerate() error {
	s.mu.Lock()
	s.calls = append(s.calls, "regen")
	s.mu.Unlock()
	return s.BaseSession.Regenerate()
}

func (s *orderTrackingSession) Put(key string, value interface{}) {
	if key == "user_id" {
		s.mu.Lock()
		s.calls = append(s.calls, "put-user_id")
		s.mu.Unlock()
	}
	s.BaseSession.Put(key, value)
}

func (s *orderTrackingSession) ordering() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

// orderTrackingStore wraps the cookie store but returns our tracking
// session on Get so the anchorRecalledUser path operates on it.
type orderTrackingStore struct {
	inner   auth.SessionStore
	created *orderTrackingSession
	mu      sync.Mutex
}

func (s *orderTrackingStore) Create(id string) (auth.Session, error) {
	sess := newOrderTrackingSession(id)
	s.mu.Lock()
	s.created = sess
	s.mu.Unlock()
	return sess, nil
}

func (s *orderTrackingStore) Get(r *http.Request, id string) (auth.Session, error) {
	return s.Create("")
}

func (s *orderTrackingStore) Save(w http.ResponseWriter, session auth.Session) error {
	return nil
}

func (s *orderTrackingStore) Destroy(string) error                  { return nil }
func (s *orderTrackingStore) GarbageCollect(_ time.Duration) error  { return nil }

func (s *orderTrackingStore) tracking() *orderTrackingSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.created
}

// TestSessionGuard_RememberRevival_RegenBeforePut covers audit M-09:
// the remember-cookie revival path MUST call Session.Regenerate strictly
// before writing user_id, otherwise a planted session id is reused as
// the anchored authenticated id (session fixation). Both User() and
// CheckWithError() reach the same anchorRecalledUser helper post-H-08;
// pin the ordering directly via an instrumented session.
func TestSessionGuard_RememberRevival_RegenBeforePut(t *testing.T) {
	guard, _ := newRevokeGuard(t, nil)
	provider := &rememberRevivalProvider{user: &revokeTestUser{id: "u1"}}
	guard.SetProvider(provider)

	// Mint the remember cookie via a real Login (uses the real store).
	rememberCookie := mintRememberCookie(t, guard)

	// Now swap in the tracking store so the revival path operates on
	// our instrumented session.
	tracker := &orderTrackingStore{inner: guard.store}
	guard.store = tracker

	for _, method := range []string{"User", "Check"} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(rememberCookie)
			req = WithSessionContext(req)

			switch method {
			case "User":
				if u := guard.User(req); u == nil {
					t.Fatal("User returned nil")
				}
			case "Check":
				if !guard.Check(req) {
					t.Fatal("Check returned false")
				}
			}

			sess := tracker.tracking()
			if sess == nil {
				t.Fatal("tracker captured no session")
			}
			calls := sess.ordering()
			if len(calls) < 2 {
				t.Fatalf("expected >=2 ordering events (regen, put-user_id), got %v", calls)
			}
			// Find the first put-user_id and assert at least one regen
			// happened strictly before it.
			firstPut := -1
			for i, c := range calls {
				if c == "put-user_id" {
					firstPut = i
					break
				}
			}
			if firstPut <= 0 {
				t.Fatalf("put-user_id not preceded by regen in %v", calls)
			}
			seenRegen := false
			for _, c := range calls[:firstPut] {
				if c == "regen" {
					seenRegen = true
					break
				}
			}
			if !seenRegen {
				t.Errorf("Regenerate must be called before Put(\"user_id\"); calls = %v", calls)
			}
		})
	}
}
