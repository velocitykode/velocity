package guards

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/pkg/auth"
)

// mockSession implements auth.Session for testing
type mockSession struct {
	id    string
	data  map[string]interface{}
	flash map[string]interface{}
}

func newMockSession() *mockSession {
	return &mockSession{
		id:    "test-session-id",
		data:  make(map[string]interface{}),
		flash: make(map[string]interface{}),
	}
}

func (s *mockSession) ID() string                          { return s.id }
func (s *mockSession) Get(key string) interface{}          { return s.data[key] }
func (s *mockSession) Put(key string, value interface{})   { s.data[key] = value }
func (s *mockSession) Has(key string) bool                 { _, ok := s.data[key]; return ok }
func (s *mockSession) Remove(key string)                   { delete(s.data, key) }
func (s *mockSession) Clear()                              { s.data = make(map[string]interface{}) }
func (s *mockSession) Regenerate() error                   { return nil }
func (s *mockSession) Invalidate() error                   { s.data = make(map[string]interface{}); return nil }
func (s *mockSession) Flash(key string, value interface{}) { s.flash[key] = value }
func (s *mockSession) GetFlash(key string) interface{} {
	v := s.flash[key]
	delete(s.flash, key)
	return v
}
func (s *mockSession) Save(w http.ResponseWriter) error { return nil }

// mockSessionStore implements auth.SessionStore for testing
type mockSessionStore struct{}

func (s *mockSessionStore) Create(id string) (auth.Session, error) {
	return newMockSession(), nil
}

func (s *mockSessionStore) Get(r *http.Request, id string) (auth.Session, error) {
	return newMockSession(), nil
}

func (s *mockSessionStore) Save(w http.ResponseWriter, session auth.Session) error {
	return nil
}

func (s *mockSessionStore) Destroy(id string) error {
	return nil
}

func (s *mockSessionStore) GarbageCollect(maxLifetime time.Duration) error {
	return nil
}

// mockUserProvider implements auth.UserProvider for testing
type mockUserProvider struct{}

func (p *mockUserProvider) FindByID(id interface{}) (auth.Authenticatable, error) {
	return &auth.AuthUser{ID: id, Name: "Test", Email: "test@test.com"}, nil
}

func (p *mockUserProvider) FindByCredentials(credentials map[string]interface{}) (auth.Authenticatable, error) {
	return &auth.AuthUser{ID: 1, Name: "Test", Email: "test@test.com"}, nil
}

func (p *mockUserProvider) ValidateCredentials(user auth.Authenticatable, credentials map[string]interface{}) bool {
	return true
}

func (p *mockUserProvider) UpdateRememberToken(user auth.Authenticatable, token string) error {
	return nil
}

func TestSessionGuard_ConcurrentGetSession(t *testing.T) {
	guard := &SessionGuard{
		provider: &mockUserProvider{},
		store:    &mockSessionStore{},
		config:   auth.SessionConfig{Name: "test_session"},
		hasher:   auth.NewBcryptHasher(10),
	}

	// Create multiple requests
	numRequests := 100
	requests := make([]*http.Request, numRequests)
	for i := 0; i < numRequests; i++ {
		requests[i] = httptest.NewRequest("GET", "/test", nil)
		requests[i].AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})
	}

	// Run concurrent getSession calls
	var wg sync.WaitGroup
	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(req *http.Request) {
			defer wg.Done()
			// Call getSession multiple times concurrently
			for j := 0; j < 10; j++ {
				session := guard.getSession(req)
				if session == nil {
					t.Error("getSession returned nil")
				}
			}
		}(requests[i])
	}

	wg.Wait()
}

func TestSessionGuard_ConcurrentCheck(t *testing.T) {
	guard := &SessionGuard{
		provider: &mockUserProvider{},
		store:    &mockSessionStore{},
		config:   auth.SessionConfig{Name: "test_session"},
		hasher:   auth.NewBcryptHasher(10),
	}

	// Create a request with session containing user_id
	req := httptest.NewRequest("GET", "/test", nil)
	req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})

	// Pre-populate session with user_id
	session := guard.getSession(req)
	if session != nil {
		session.Put("user_id", int64(1))
	}

	// Run concurrent Check calls on the same request
	var wg sync.WaitGroup
	numGoroutines := 50

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				guard.Check(req)
			}
		}()
	}

	wg.Wait()
}

func TestSessionGuard_ConcurrentMixedOperations(t *testing.T) {
	guard := &SessionGuard{
		provider: &mockUserProvider{},
		store:    &mockSessionStore{},
		config:   auth.SessionConfig{Name: "test_session"},
		hasher:   auth.NewBcryptHasher(10),
	}

	var wg sync.WaitGroup
	numGoroutines := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/test", nil)
			req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})

			// Mix of operations
			for j := 0; j < 10; j++ {
				switch j % 3 {
				case 0:
					guard.getSession(req)
				case 1:
					guard.Check(req)
				case 2:
					guard.User(req)
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestSessionGuard_RaceCondition(t *testing.T) {
	// This test verifies that concurrent access to getSession doesn't cause a race condition
	guard := &SessionGuard{
		provider: &mockUserProvider{},
		store:    &mockSessionStore{},
		config:   auth.SessionConfig{Name: "test_session"},
		hasher:   auth.NewBcryptHasher(10),
	}

	// Create a single request that will be accessed by multiple goroutines
	req := httptest.NewRequest("GET", "/test", nil)
	req.AddCookie(&http.Cookie{Name: "test_session", Value: "session-id"})

	var wg sync.WaitGroup
	numGoroutines := 100

	// All goroutines access the same request simultaneously
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				// This should not panic with "concurrent map writes"
				guard.getSession(req)
			}
		}()
	}

	wg.Wait()
}
