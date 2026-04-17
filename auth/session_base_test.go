package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

// mockSessionStore implements SessionStore interface for testing
type mockSessionStore struct {
	createFn         func(id string) (Session, error)
	getFn            func(r *http.Request, id string) (Session, error)
	saveFn           func(w http.ResponseWriter, session Session) error
	destroyFn        func(id string) error
	garbageCollectFn func(maxLifetime time.Duration) error
}

func (m *mockSessionStore) Create(id string) (Session, error) {
	if m.createFn != nil {
		return m.createFn(id)
	}
	return NewSession(id), nil
}

func (m *mockSessionStore) Get(r *http.Request, id string) (Session, error) {
	if m.getFn != nil {
		return m.getFn(r, id)
	}
	return nil, errors.New("session not found")
}

func (m *mockSessionStore) Save(w http.ResponseWriter, session Session) error {
	if m.saveFn != nil {
		return m.saveFn(w, session)
	}
	return nil
}

func (m *mockSessionStore) Destroy(id string) error {
	if m.destroyFn != nil {
		return m.destroyFn(id)
	}
	return nil
}

func (m *mockSessionStore) GarbageCollect(maxLifetime time.Duration) error {
	if m.garbageCollectFn != nil {
		return m.garbageCollectFn(maxLifetime)
	}
	return nil
}

func TestBaseSessionIsModified(t *testing.T) {
	tests := []struct {
		name   string
		action func(*BaseSession)
		want   bool
	}{
		{
			name:   "returns false for new session",
			action: func(s *BaseSession) {},
			want:   false,
		},
		{
			name: "returns true after Put",
			action: func(s *BaseSession) {
				s.Put("key", "value")
			},
			want: true,
		},
		{
			name: "returns true after Remove",
			action: func(s *BaseSession) {
				s.Put("key", "value")
				// Reset modified to test Remove specifically
				s.mu.Lock()
				s.modified = false
				s.mu.Unlock()
				s.Remove("key")
			},
			want: true,
		},
		{
			name: "returns true after Clear",
			action: func(s *BaseSession) {
				s.Clear()
			},
			want: true,
		},
		{
			name: "returns true after Regenerate",
			action: func(s *BaseSession) {
				s.Regenerate()
			},
			want: true,
		},
		{
			name: "returns true after Flash",
			action: func(s *BaseSession) {
				s.Flash("key", "value")
			},
			want: true,
		},
		{
			name: "returns true after GetFlash consumes value",
			action: func(s *BaseSession) {
				s.Flash("key", "value")
				s.mu.Lock()
				s.modified = false
				s.mu.Unlock()
				s.GetFlash("key")
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := NewSession("test-id")
			tt.action(session)

			if got := session.IsModified(); got != tt.want {
				t.Errorf("IsModified() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBaseSessionIsDestroyed(t *testing.T) {
	tests := []struct {
		name   string
		action func(*BaseSession)
		want   bool
	}{
		{
			name:   "returns false for new session",
			action: func(s *BaseSession) {},
			want:   false,
		},
		{
			name: "returns true after Invalidate",
			action: func(s *BaseSession) {
				s.Invalidate()
			},
			want: true,
		},
		{
			name: "returns false after Put on new session",
			action: func(s *BaseSession) {
				s.Put("key", "value")
			},
			want: false,
		},
		{
			name: "returns false after Clear",
			action: func(s *BaseSession) {
				s.Clear()
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := NewSession("test-id")
			tt.action(session)

			if got := session.IsDestroyed(); got != tt.want {
				t.Errorf("IsDestroyed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBaseSessionGetData(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*BaseSession)
		wantData map[string]interface{}
	}{
		{
			name:     "returns empty map for new session",
			setup:    func(s *BaseSession) {},
			wantData: map[string]interface{}{},
		},
		{
			name: "returns data after Put",
			setup: func(s *BaseSession) {
				s.Put("key1", "value1")
				s.Put("key2", 123)
			},
			wantData: map[string]interface{}{
				"key1": "value1",
				"key2": 123,
			},
		},
		{
			name: "returns copy of data not reference",
			setup: func(s *BaseSession) {
				s.Put("key", "value")
			},
			wantData: map[string]interface{}{
				"key": "value",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := NewSession("test-id")
			tt.setup(session)

			got := session.GetData()

			if !reflect.DeepEqual(got, tt.wantData) {
				t.Errorf("GetData() = %v, want %v", got, tt.wantData)
			}
		})
	}

	t.Run("GetData returns copy not reference", func(t *testing.T) {
		session := NewSession("test-id")
		session.Put("key", "original")

		data := session.GetData()
		data["key"] = "modified"

		// Original session data should be unchanged
		if session.Get("key") != "original" {
			t.Error("GetData() should return a copy, not a reference")
		}
	})
}

func TestBaseSessionSetData(t *testing.T) {
	tests := []struct {
		name     string
		data     map[string]interface{}
		checkKey string
		wantVal  interface{}
	}{
		{
			name: "sets data from map",
			data: map[string]interface{}{
				"key1": "value1",
				"key2": 123,
			},
			checkKey: "key1",
			wantVal:  "value1",
		},
		{
			name:     "sets empty map",
			data:     map[string]interface{}{},
			checkKey: "nonexistent",
			wantVal:  nil,
		},
		{
			name:     "sets nil map",
			data:     nil,
			checkKey: "nonexistent",
			wantVal:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := NewSession("test-id")
			session.SetData(tt.data)

			got := session.Get(tt.checkKey)
			if got != tt.wantVal {
				t.Errorf("Get(%q) after SetData = %v, want %v", tt.checkKey, got, tt.wantVal)
			}
		})
	}

	t.Run("SetData replaces existing data", func(t *testing.T) {
		session := NewSession("test-id")
		session.Put("original", "value")

		session.SetData(map[string]interface{}{
			"new": "data",
		})

		if session.Has("original") {
			t.Error("SetData should replace existing data")
		}
		if !session.Has("new") {
			t.Error("SetData should set new data")
		}
	})
}

func TestBaseSessionGetFlashData(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*BaseSession)
		wantFlash map[string]interface{}
	}{
		{
			name:      "returns empty map for new session",
			setup:     func(s *BaseSession) {},
			wantFlash: map[string]interface{}{},
		},
		{
			name: "returns flash data after Flash",
			setup: func(s *BaseSession) {
				s.Flash("message", "Success!")
				s.Flash("error", "Failed!")
			},
			wantFlash: map[string]interface{}{
				"message": "Success!",
				"error":   "Failed!",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := NewSession("test-id")
			tt.setup(session)

			got := session.GetFlashData()

			if !reflect.DeepEqual(got, tt.wantFlash) {
				t.Errorf("GetFlashData() = %v, want %v", got, tt.wantFlash)
			}
		})
	}

	t.Run("GetFlashData returns copy not reference", func(t *testing.T) {
		session := NewSession("test-id")
		session.Flash("key", "original")

		flash := session.GetFlashData()
		flash["key"] = "modified"

		// Original flash data should be unchanged
		originalFlash := session.GetFlashData()
		if originalFlash["key"] != "original" {
			t.Error("GetFlashData() should return a copy, not a reference")
		}
	})
}

func TestBaseSessionSetFlashData(t *testing.T) {
	tests := []struct {
		name      string
		flashData map[string]interface{}
		checkKey  string
		wantVal   interface{}
	}{
		{
			name: "sets flash data from map",
			flashData: map[string]interface{}{
				"message": "Hello",
				"count":   42,
			},
			checkKey: "message",
			wantVal:  "Hello",
		},
		{
			name:      "sets empty flash map",
			flashData: map[string]interface{}{},
			checkKey:  "nonexistent",
			wantVal:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := NewSession("test-id")
			session.SetFlashData(tt.flashData)

			got := session.GetFlash(tt.checkKey)
			if got != tt.wantVal {
				t.Errorf("GetFlash(%q) after SetFlashData = %v, want %v", tt.checkKey, got, tt.wantVal)
			}
		})
	}

	t.Run("SetFlashData replaces existing flash", func(t *testing.T) {
		session := NewSession("test-id")
		session.Flash("original", "value")

		session.SetFlashData(map[string]interface{}{
			"new": "flash",
		})

		originalFlash := session.GetFlashData()
		if _, exists := originalFlash["original"]; exists {
			t.Error("SetFlashData should replace existing flash data")
		}
		if _, exists := originalFlash["new"]; !exists {
			t.Error("SetFlashData should set new flash data")
		}
	})
}

func TestGetSessionFromRequest(t *testing.T) {
	tests := []struct {
		name        string
		setupReq    func() *http.Request
		store       *mockSessionStore
		cookieName  string
		wantNewSess bool
		wantErr     bool
	}{
		{
			name: "creates new session when no cookie",
			setupReq: func() *http.Request {
				return httptest.NewRequest("GET", "/", nil)
			},
			store:       &mockSessionStore{},
			cookieName:  "session",
			wantNewSess: true,
			wantErr:     false,
		},
		{
			name: "gets existing session from cookie",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{
					Name:  "session",
					Value: "existing-session-id",
				})
				return req
			},
			store: &mockSessionStore{
				getFn: func(r *http.Request, id string) (Session, error) {
					if id == "existing-session-id" {
						s := NewSession(id)
						s.Put("user_id", 123)
						return s, nil
					}
					return nil, errors.New("not found")
				},
			},
			cookieName:  "session",
			wantNewSess: false,
			wantErr:     false,
		},
		{
			name: "creates new session when store returns error",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{
					Name:  "session",
					Value: "invalid-session-id",
				})
				return req
			},
			store: &mockSessionStore{
				getFn: func(r *http.Request, id string) (Session, error) {
					return nil, errors.New("session expired")
				},
			},
			cookieName:  "session",
			wantNewSess: true,
			wantErr:     false,
		},
		{
			name: "creates new session with different cookie name",
			setupReq: func() *http.Request {
				req := httptest.NewRequest("GET", "/", nil)
				req.AddCookie(&http.Cookie{
					Name:  "other_cookie",
					Value: "some-value",
				})
				return req
			},
			store:       &mockSessionStore{},
			cookieName:  "velocity_session",
			wantNewSess: true,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.setupReq()

			session, err := GetSessionFromRequest(req, tt.store, tt.cookieName)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetSessionFromRequest() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if session == nil {
				t.Error("GetSessionFromRequest() returned nil session")
				return
			}

			if tt.wantNewSess {
				// New sessions should have empty data
				data := session.(*BaseSession).GetData()
				if len(data) != 0 {
					t.Error("Expected new session with empty data")
				}
			}
		})
	}
}

func TestBaseSessionConcurrency(t *testing.T) {
	session := NewSession("test-id")

	var wg sync.WaitGroup
	numGoroutines := 100

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			session.Put("key_"+string(rune('a'+idx%26)), idx)
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = session.Get("key_" + string(rune('a'+idx%26)))
		}(i)
	}

	// Concurrent flash operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			session.Flash("flash_"+string(rune('a'+idx%26)), idx)
		}(i)
	}

	// Concurrent status checks
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = session.IsModified()
			_ = session.IsDestroyed()
		}()
	}

	// Concurrent data retrieval
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = session.GetData()
			_ = session.GetFlashData()
		}()
	}

	wg.Wait()
}

func TestNewSessionGeneratesID(t *testing.T) {
	t.Run("generates ID when empty string provided", func(t *testing.T) {
		session := NewSession("")
		if session.ID() == "" {
			t.Error("NewSession(\"\") should generate a non-empty ID")
		}
	})

	t.Run("uses provided ID when non-empty", func(t *testing.T) {
		session := NewSession("my-custom-id")
		if session.ID() != "my-custom-id" {
			t.Errorf("NewSession(id) should use provided ID, got %v", session.ID())
		}
	})

	t.Run("generates unique IDs", func(t *testing.T) {
		ids := make(map[string]bool)
		for i := 0; i < 100; i++ {
			session := NewSession("")
			id := session.ID()
			if ids[id] {
				t.Errorf("Generated duplicate session ID: %v", id)
			}
			ids[id] = true
		}
	})
}

func TestBaseSessionSave(t *testing.T) {
	// Base session Save is a no-op but should not error
	session := NewSession("test-id")
	rec := httptest.NewRecorder()

	err := session.Save(rec)
	if err != nil {
		t.Errorf("Save() error = %v, want nil", err)
	}
}

func TestSessionInterfaceImplementation(t *testing.T) {
	// Compile-time check that BaseSession implements Session
	var _ Session = (*BaseSession)(nil)

	t.Run("BaseSession implements Session interface", func(t *testing.T) {
		session := NewSession("test-id")

		// Test all interface methods work
		_ = session.ID()
		session.Put("key", "value")
		_ = session.Get("key")
		_ = session.Has("key")
		session.Remove("key")
		session.Clear()
		_ = session.Regenerate()
		_ = session.Invalidate()
		session.Flash("flash", "value")
		_ = session.GetFlash("flash")
		_ = session.Save(httptest.NewRecorder())
	})
}

func TestSessionStoreInterfaceImplementation(t *testing.T) {
	// Compile-time check that mockSessionStore implements SessionStore
	var _ SessionStore = (*mockSessionStore)(nil)
}
