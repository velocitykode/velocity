package session

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/crypto"
)

// mockEncryptor implements crypto.Encryptor for testing
type mockEncryptor struct {
	encryptFunc func(string) (string, error)
	decryptFunc func(string) (string, error)
}

func (m *mockEncryptor) Encrypt(plaintext string) (string, error) {
	if m.encryptFunc != nil {
		return m.encryptFunc(plaintext)
	}
	return plaintext, nil
}

func (m *mockEncryptor) EncryptBytes(plaintext []byte) (string, error) {
	return m.Encrypt(string(plaintext))
}

func (m *mockEncryptor) Decrypt(payload string) (string, error) {
	if m.decryptFunc != nil {
		return m.decryptFunc(payload)
	}
	return payload, nil
}

func (m *mockEncryptor) DecryptBytes(payload string) ([]byte, error) {
	result, err := m.Decrypt(payload)
	return []byte(result), err
}

func (m *mockEncryptor) EncryptBytesWithAAD(plaintext, _ []byte) (string, error) {
	return m.EncryptBytes(plaintext)
}

func (m *mockEncryptor) DecryptBytesWithAAD(payload string, _ []byte) ([]byte, error) {
	return m.DecryptBytes(payload)
}

func (m *mockEncryptor) GenerateKey() (string, error) {
	return "test-key-32-bytes-long-for-test!", nil
}

// testConfig returns a standard test configuration
func testConfig() auth.SessionConfig {
	return auth.SessionConfig{
		Name:     "test_session",
		Lifetime: 120,
		Path:     "/",
		Domain:   "",
		Secure:   false,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

// newTestCookieStore creates a CookieStore with a mock encryptor for testing
func newTestCookieStore(config auth.SessionConfig, enc *mockEncryptor) *CookieStore {
	return &CookieStore{
		config:    config,
		encryptor: enc,
	}
}

func TestNewCookieStore(t *testing.T) {
	tests := []struct {
		name        string
		config      auth.SessionConfig
		encryptor   crypto.Encryptor
		wantErr     bool
		checkResult func(t *testing.T, store *CookieStore)
	}{
		{
			name:   "creates store with provided encryptor",
			config: testConfig(),
			encryptor: func() crypto.Encryptor {
				enc, err := crypto.NewEncryptor(crypto.Config{
					Key:    "test-key-32-bytes-long-for-test!",
					Cipher: "AES-256-CBC",
				})
				if err != nil {
					t.Fatalf("Failed to create encryptor: %v", err)
				}
				return enc
			}(),
			wantErr: false,
			checkResult: func(t *testing.T, store *CookieStore) {
				if store == nil {
					t.Fatal("expected store to be non-nil")
				}
				if store.encryptor == nil {
					t.Error("expected encryptor to be set")
				}
			},
		},
		{
			name:      "returns error when encryptor is nil",
			config:    testConfig(),
			encryptor: nil,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewCookieStore(tt.config, tt.encryptor)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewCookieStore() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.checkResult != nil && got != nil {
				tt.checkResult(t, got)
			}
		})
	}
}

func TestCookieStore_Create(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
		check   func(t *testing.T, session auth.Session)
	}{
		{
			name:    "creates session with provided ID",
			id:      "test-session-id",
			wantErr: false,
			check: func(t *testing.T, session auth.Session) {
				if session.ID() != "test-session-id" {
					t.Errorf("expected ID 'test-session-id', got '%s'", session.ID())
				}
			},
		},
		{
			name:    "creates session with generated ID when empty",
			id:      "",
			wantErr: false,
			check: func(t *testing.T, session auth.Session) {
				if session.ID() == "" {
					t.Error("expected generated ID, got empty string")
				}
			},
		},
		{
			name:    "returns CookieSession type",
			id:      "type-check-id",
			wantErr: false,
			check: func(t *testing.T, session auth.Session) {
				_, ok := session.(*CookieSession)
				if !ok {
					t.Errorf("expected *CookieSession type, got %T", session)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestCookieStore(testConfig(), &mockEncryptor{})

			got, err := store.Create(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("Create() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestCookieStore_Get(t *testing.T) {
	tests := []struct {
		name      string
		setupReq  func() *http.Request
		encryptor *mockEncryptor
		wantErr   bool
		check     func(t *testing.T, session auth.Session)
	}{
		{
			name: "returns new session when no cookie present",
			setupReq: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/", nil)
			},
			encryptor: &mockEncryptor{},
			wantErr:   false,
			check: func(t *testing.T, session auth.Session) {
				if session == nil {
					t.Error("expected session, got nil")
				}
				if session.ID() == "" {
					t.Error("expected generated session ID")
				}
			},
		},
		{
			name: "returns restored session with valid encrypted cookie",
			setupReq: func() *http.Request {
				sessionData := struct {
					ID    string                 `json:"id"`
					Data  map[string]interface{} `json:"data"`
					Flash map[string]interface{} `json:"flash"`
				}{
					ID:    "restored-session-id",
					Data:  map[string]interface{}{"user": "john"},
					Flash: map[string]interface{}{"message": "welcome"},
				}
				jsonData, _ := json.Marshal(sessionData)
				// Base64 encode to make it cookie-safe
				encodedValue := base64.URLEncoding.EncodeToString(jsonData)

				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.AddCookie(&http.Cookie{
					Name:  "test_session",
					Value: encodedValue,
				})
				return req
			},
			encryptor: &mockEncryptor{
				decryptFunc: func(payload string) (string, error) {
					// Decode base64 first, then return the JSON
					decoded, err := base64.URLEncoding.DecodeString(payload)
					if err != nil {
						return "", err
					}
					return string(decoded), nil
				},
			},
			wantErr: false,
			check: func(t *testing.T, session auth.Session) {
				if session.ID() != "restored-session-id" {
					t.Errorf("expected ID 'restored-session-id', got '%s'", session.ID())
				}
				if session.Get("user") != "john" {
					t.Errorf("expected user 'john', got '%v'", session.Get("user"))
				}
				if session.GetFlash("message") != "welcome" {
					t.Errorf("expected flash message 'welcome', got '%v'", session.GetFlash("message"))
				}
			},
		},
		{
			name: "returns new session when decryption fails",
			setupReq: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.AddCookie(&http.Cookie{
					Name:  "test_session",
					Value: "corrupted-encrypted-data",
				})
				return req
			},
			encryptor: &mockEncryptor{
				decryptFunc: func(payload string) (string, error) {
					return "", errors.New("decryption failed")
				},
			},
			wantErr: false,
			check: func(t *testing.T, session auth.Session) {
				if session == nil {
					t.Error("expected new session, got nil")
				}
				// Should have new generated ID, not the corrupted one
				if session.ID() == "" {
					t.Error("expected generated session ID")
				}
			},
		},
		{
			name: "returns new session when JSON is invalid",
			setupReq: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.AddCookie(&http.Cookie{
					Name:  "test_session",
					Value: "not-json-data",
				})
				return req
			},
			encryptor: &mockEncryptor{
				decryptFunc: func(payload string) (string, error) {
					return "not-valid-json", nil
				},
			},
			wantErr: false,
			check: func(t *testing.T, session auth.Session) {
				if session == nil {
					t.Error("expected new session, got nil")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestCookieStore(testConfig(), tt.encryptor)
			req := tt.setupReq()

			got, err := store.Get(req, "")
			if (err != nil) != tt.wantErr {
				t.Errorf("Get() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestCookieStore_Get_RoundTrip(t *testing.T) {
	encryptor := &mockEncryptor{
		encryptFunc: func(plaintext string) (string, error) {
			// Use base64 encoding to make the value cookie-safe
			return base64.URLEncoding.EncodeToString([]byte(plaintext)), nil
		},
		decryptFunc: func(payload string) (string, error) {
			decoded, err := base64.URLEncoding.DecodeString(payload)
			if err != nil {
				return "", err
			}
			return string(decoded), nil
		},
	}
	store := newTestCookieStore(testConfig(), encryptor)

	// Create and save a session
	session, err := store.Create("roundtrip-id")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	session.Put("key1", "value1")
	session.Flash("flash1", "flashvalue1")

	recorder := httptest.NewRecorder()
	err = store.Save(recorder, session)
	if err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Get the cookie from the response
	resp := recorder.Result()
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected cookie to be set")
	}

	// Create a new request with the cookie
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookies[0])

	// Get the session back
	restoredSession, err := store.Get(req, "")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}

	if restoredSession.ID() != "roundtrip-id" {
		t.Errorf("expected ID 'roundtrip-id', got '%s'", restoredSession.ID())
	}
	if restoredSession.Get("key1") != "value1" {
		t.Errorf("expected key1 'value1', got '%v'", restoredSession.Get("key1"))
	}
}

func TestCookieStore_Save(t *testing.T) {
	tests := []struct {
		name      string
		session   func(store *CookieStore) auth.Session
		encryptor *mockEncryptor
		wantErr   bool
		errType   error
		check     func(t *testing.T, resp *http.Response)
	}{
		{
			name: "saves CookieSession type",
			session: func(store *CookieStore) auth.Session {
				session, _ := store.Create("cookie-session-id")
				session.Put("data", "value")
				return session
			},
			encryptor: &mockEncryptor{},
			wantErr:   false,
			check: func(t *testing.T, resp *http.Response) {
				cookies := resp.Cookies()
				if len(cookies) == 0 {
					t.Error("expected cookie to be set")
					return
				}
				if cookies[0].Name != "test_session" {
					t.Errorf("expected cookie name 'test_session', got '%s'", cookies[0].Name)
				}
			},
		},
		{
			name: "saves BaseSession type by wrapping",
			session: func(store *CookieStore) auth.Session {
				s := auth.NewSession("base-session-id")
				s.Put("k", "v") // mark modified so Save persists
				return s
			},
			encryptor: &mockEncryptor{},
			wantErr:   false,
			check: func(t *testing.T, resp *http.Response) {
				cookies := resp.Cookies()
				if len(cookies) == 0 {
					t.Error("expected cookie to be set")
				}
			},
		},
		{
			name: "returns error for unsupported session type",
			session: func(store *CookieStore) auth.Session {
				return &unsupportedSession{}
			},
			encryptor: &mockEncryptor{},
			wantErr:   true,
			errType:   auth.ErrInvalidSession,
			check:     nil,
		},
		{
			name: "sets MaxAge -1 for destroyed session",
			session: func(store *CookieStore) auth.Session {
				session, _ := store.Create("destroyed-session-id")
				_ = session.Invalidate()
				return session
			},
			encryptor: &mockEncryptor{},
			wantErr:   false,
			check: func(t *testing.T, resp *http.Response) {
				cookies := resp.Cookies()
				if len(cookies) == 0 {
					t.Error("expected cookie to be set")
					return
				}
				if cookies[0].MaxAge != -1 {
					t.Errorf("expected MaxAge -1, got %d", cookies[0].MaxAge)
				}
				if cookies[0].Value != "" {
					t.Errorf("expected empty cookie value, got '%s'", cookies[0].Value)
				}
			},
		},
		{
			name: "returns error when encryption fails",
			session: func(store *CookieStore) auth.Session {
				session, _ := store.Create("encryption-fail-id")
				session.Put("k", "v") // force modified so Encrypt is invoked
				return session
			},
			encryptor: &mockEncryptor{
				encryptFunc: func(plaintext string) (string, error) {
					return "", errors.New("encryption failed")
				},
			},
			wantErr: true,
			check:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestCookieStore(testConfig(), tt.encryptor)
			session := tt.session(store)

			recorder := httptest.NewRecorder()
			err := store.Save(recorder, session)

			if (err != nil) != tt.wantErr {
				t.Errorf("Save() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.errType != nil && !errors.Is(err, tt.errType) {
				t.Errorf("Save() error = %v, want %v", err, tt.errType)
			}

			if tt.check != nil {
				resp := recorder.Result()
				tt.check(t, resp)
			}
		})
	}
}

func TestCookieStore_Save_CookieAttributes(t *testing.T) {
	tests := []struct {
		name   string
		config auth.SessionConfig
		check  func(t *testing.T, cookie *http.Cookie)
	}{
		{
			name: "sets HttpOnly attribute",
			config: auth.SessionConfig{
				Name:     "test_session",
				Lifetime: 120,
				Path:     "/",
				HttpOnly: true,
			},
			check: func(t *testing.T, cookie *http.Cookie) {
				if !cookie.HttpOnly {
					t.Error("expected HttpOnly to be true")
				}
			},
		},
		{
			name: "sets Secure attribute",
			config: auth.SessionConfig{
				Name:     "test_session",
				Lifetime: 120,
				Path:     "/",
				Secure:   true,
			},
			check: func(t *testing.T, cookie *http.Cookie) {
				if !cookie.Secure {
					t.Error("expected Secure to be true")
				}
			},
		},
		{
			name: "sets SameSite strict mode",
			config: auth.SessionConfig{
				Name:     "test_session",
				Lifetime: 120,
				Path:     "/",
				SameSite: http.SameSiteStrictMode,
			},
			check: func(t *testing.T, cookie *http.Cookie) {
				if cookie.SameSite != http.SameSiteStrictMode {
					t.Errorf("expected SameSite Strict, got %v", cookie.SameSite)
				}
			},
		},
		{
			name: "sets Path attribute",
			config: auth.SessionConfig{
				Name:     "test_session",
				Lifetime: 120,
				Path:     "/custom/path",
			},
			check: func(t *testing.T, cookie *http.Cookie) {
				if cookie.Path != "/custom/path" {
					t.Errorf("expected Path '/custom/path', got '%s'", cookie.Path)
				}
			},
		},
		{
			name: "sets Domain attribute",
			config: auth.SessionConfig{
				Name:     "test_session",
				Lifetime: 120,
				Path:     "/",
				Domain:   "example.com",
			},
			check: func(t *testing.T, cookie *http.Cookie) {
				if cookie.Domain != "example.com" {
					t.Errorf("expected Domain 'example.com', got '%s'", cookie.Domain)
				}
			},
		},
		{
			name: "sets MaxAge based on Lifetime in minutes",
			config: auth.SessionConfig{
				Name:     "test_session",
				Lifetime: 60, // 60 minutes
				Path:     "/",
			},
			check: func(t *testing.T, cookie *http.Cookie) {
				expectedMaxAge := 60 * 60 // 60 minutes * 60 seconds
				if cookie.MaxAge != expectedMaxAge {
					t.Errorf("expected MaxAge %d, got %d", expectedMaxAge, cookie.MaxAge)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestCookieStore(tt.config, &mockEncryptor{})
			session, _ := store.Create("attr-test-id")
			session.Put("k", "v") // mark modified so Save persists

			recorder := httptest.NewRecorder()
			err := store.Save(recorder, session)
			if err != nil {
				t.Fatalf("Save() error = %v", err)
			}

			resp := recorder.Result()
			cookies := resp.Cookies()
			if len(cookies) == 0 {
				t.Fatal("expected cookie to be set")
			}

			tt.check(t, cookies[0])
		})
	}
}

func TestCookieStore_Destroy(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{
			name:    "returns nil for any ID",
			id:      "any-session-id",
			wantErr: false,
		},
		{
			name:    "returns nil for empty ID",
			id:      "",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestCookieStore(testConfig(), &mockEncryptor{})

			err := store.Destroy(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("Destroy() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCookieStore_GarbageCollect(t *testing.T) {
	tests := []struct {
		name        string
		maxLifetime time.Duration
		wantErr     bool
	}{
		{
			name:        "returns nil for any lifetime",
			maxLifetime: 24 * time.Hour,
			wantErr:     false,
		},
		{
			name:        "returns nil for zero lifetime",
			maxLifetime: 0,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestCookieStore(testConfig(), &mockEncryptor{})

			err := store.GarbageCollect(tt.maxLifetime)
			if (err != nil) != tt.wantErr {
				t.Errorf("GarbageCollect() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCookieSession_Save(t *testing.T) {
	tests := []struct {
		name      string
		setupFunc func(store *CookieStore) *CookieSession
		wantErr   bool
		check     func(t *testing.T, resp *http.Response)
	}{
		{
			name: "delegates to CookieStore.Save",
			setupFunc: func(store *CookieStore) *CookieSession {
				session, _ := store.Create("delegate-id")
				session.Put("k", "v") // mark modified so Save persists
				return session.(*CookieSession)
			},
			wantErr: false,
			check: func(t *testing.T, resp *http.Response) {
				cookies := resp.Cookies()
				if len(cookies) == 0 {
					t.Error("expected cookie to be set")
				}
			},
		},
		{
			name: "saves session data correctly",
			setupFunc: func(store *CookieStore) *CookieSession {
				session, _ := store.Create("data-id")
				session.Put("testKey", "testValue")
				return session.(*CookieSession)
			},
			wantErr: false,
			check: func(t *testing.T, resp *http.Response) {
				cookies := resp.Cookies()
				if len(cookies) == 0 {
					t.Error("expected cookie to be set")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestCookieStore(testConfig(), &mockEncryptor{})
			session := tt.setupFunc(store)

			recorder := httptest.NewRecorder()
			err := session.Save(recorder)

			if (err != nil) != tt.wantErr {
				t.Errorf("CookieSession.Save() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.check != nil {
				resp := recorder.Result()
				tt.check(t, resp)
			}
		})
	}
}

func TestCookieSession_Implements_Session(t *testing.T) {
	store := newTestCookieStore(testConfig(), &mockEncryptor{})
	session, err := store.Create("interface-test")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Verify CookieSession implements auth.Session
	var _ auth.Session = session

	// Test that all Session methods work
	tests := []struct {
		name string
		test func(t *testing.T, s auth.Session)
	}{
		{
			name: "ID returns session ID",
			test: func(t *testing.T, s auth.Session) {
				if s.ID() != "interface-test" {
					t.Errorf("expected ID 'interface-test', got '%s'", s.ID())
				}
			},
		},
		{
			name: "Put and Get work correctly",
			test: func(t *testing.T, s auth.Session) {
				s.Put("testKey", "testValue")
				if s.Get("testKey") != "testValue" {
					t.Errorf("expected 'testValue', got '%v'", s.Get("testKey"))
				}
			},
		},
		{
			name: "Has returns true for existing key",
			test: func(t *testing.T, s auth.Session) {
				s.Put("existsKey", "value")
				if !s.Has("existsKey") {
					t.Error("expected Has() to return true")
				}
			},
		},
		{
			name: "Has returns false for non-existing key",
			test: func(t *testing.T, s auth.Session) {
				if s.Has("nonExistentKey") {
					t.Error("expected Has() to return false")
				}
			},
		},
		{
			name: "Remove deletes key",
			test: func(t *testing.T, s auth.Session) {
				s.Put("removeKey", "value")
				s.Remove("removeKey")
				if s.Has("removeKey") {
					t.Error("expected key to be removed")
				}
			},
		},
		{
			name: "Flash and GetFlash work correctly",
			test: func(t *testing.T, s auth.Session) {
				s.Flash("flashKey", "flashValue")
				value := s.GetFlash("flashKey")
				if value != "flashValue" {
					t.Errorf("expected 'flashValue', got '%v'", value)
				}
				// Flash should be removed after first get
				if s.GetFlash("flashKey") != nil {
					t.Error("expected flash to be removed after first get")
				}
			},
		},
		{
			name: "Regenerate generates new ID",
			test: func(t *testing.T, s auth.Session) {
				oldID := s.ID()
				err := s.Regenerate()
				if err != nil {
					t.Errorf("Regenerate() error = %v", err)
				}
				if s.ID() == oldID {
					t.Error("expected new ID after regenerate")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fresh session for each test
			session, _ := store.Create("interface-test")
			tt.test(t, session)
		})
	}
}

func TestCookieStore_SessionDataPersistence(t *testing.T) {
	encryptor := &mockEncryptor{
		encryptFunc: func(plaintext string) (string, error) {
			// Use base64 encoding to make the value cookie-safe
			return base64.URLEncoding.EncodeToString([]byte(plaintext)), nil
		},
		decryptFunc: func(payload string) (string, error) {
			decoded, err := base64.URLEncoding.DecodeString(payload)
			if err != nil {
				return "", errors.New("invalid encrypted payload")
			}
			return string(decoded), nil
		},
	}
	store := newTestCookieStore(testConfig(), encryptor)

	tests := []struct {
		name  string
		setup func(session auth.Session)
		check func(t *testing.T, restored auth.Session)
	}{
		{
			name: "persists string data",
			setup: func(session auth.Session) {
				session.Put("stringKey", "stringValue")
			},
			check: func(t *testing.T, restored auth.Session) {
				if restored.Get("stringKey") != "stringValue" {
					t.Errorf("expected 'stringValue', got '%v'", restored.Get("stringKey"))
				}
			},
		},
		{
			name: "persists numeric data",
			setup: func(session auth.Session) {
				session.Put("numKey", float64(42)) // JSON unmarshals numbers as float64
			},
			check: func(t *testing.T, restored auth.Session) {
				val := restored.Get("numKey")
				if val != float64(42) {
					t.Errorf("expected 42, got '%v' (type: %T)", val, val)
				}
			},
		},
		{
			name: "persists flash data",
			setup: func(session auth.Session) {
				session.Flash("flashKey", "flashValue")
			},
			check: func(t *testing.T, restored auth.Session) {
				if restored.GetFlash("flashKey") != "flashValue" {
					t.Errorf("expected flash 'flashValue', got '%v'", restored.GetFlash("flashKey"))
				}
			},
		},
		{
			name: "persists multiple values",
			setup: func(session auth.Session) {
				session.Put("key1", "value1")
				session.Put("key2", "value2")
				session.Put("key3", "value3")
			},
			check: func(t *testing.T, restored auth.Session) {
				if restored.Get("key1") != "value1" {
					t.Errorf("expected key1='value1', got '%v'", restored.Get("key1"))
				}
				if restored.Get("key2") != "value2" {
					t.Errorf("expected key2='value2', got '%v'", restored.Get("key2"))
				}
				if restored.Get("key3") != "value3" {
					t.Errorf("expected key3='value3', got '%v'", restored.Get("key3"))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create session
			session, _ := store.Create("persist-test")
			tt.setup(session)

			// Save session
			recorder := httptest.NewRecorder()
			err := store.Save(recorder, session)
			if err != nil {
				t.Fatalf("Save() error = %v", err)
			}

			// Restore session
			cookies := recorder.Result().Cookies()
			if len(cookies) == 0 {
				t.Fatal("no cookies set")
			}

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(cookies[0])

			restored, err := store.Get(req, "")
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}

			tt.check(t, restored)
		})
	}
}

func TestCookieStore_EdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		test      func(t *testing.T, store *CookieStore)
		encryptor *mockEncryptor
	}{
		{
			name:      "handles nil data in session",
			encryptor: &mockEncryptor{},
			test: func(t *testing.T, store *CookieStore) {
				session, _ := store.Create("nil-test")
				session.Put("nilKey", nil)

				recorder := httptest.NewRecorder()
				err := store.Save(recorder, session)
				if err != nil {
					t.Errorf("Save() with nil value error = %v", err)
				}
			},
		},
		{
			name:      "handles empty string values",
			encryptor: &mockEncryptor{},
			test: func(t *testing.T, store *CookieStore) {
				session, _ := store.Create("empty-test")
				session.Put("emptyKey", "")

				recorder := httptest.NewRecorder()
				err := store.Save(recorder, session)
				if err != nil {
					t.Errorf("Save() with empty string error = %v", err)
				}
			},
		},
		{
			name:      "handles special characters in values",
			encryptor: &mockEncryptor{},
			test: func(t *testing.T, store *CookieStore) {
				session, _ := store.Create("special-test")
				session.Put("specialKey", "value with \"quotes\" and 'apostrophes' and \n newlines")

				recorder := httptest.NewRecorder()
				err := store.Save(recorder, session)
				if err != nil {
					t.Errorf("Save() with special characters error = %v", err)
				}
			},
		},
		{
			name:      "handles unicode values",
			encryptor: &mockEncryptor{},
			test: func(t *testing.T, store *CookieStore) {
				session, _ := store.Create("unicode-test")
				session.Put("unicodeKey", "Hello World")

				recorder := httptest.NewRecorder()
				err := store.Save(recorder, session)
				if err != nil {
					t.Errorf("Save() with unicode error = %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestCookieStore(testConfig(), tt.encryptor)
			tt.test(t, store)
		})
	}
}

// unsupportedSession is a mock session type that is not supported by CookieStore
type unsupportedSession struct{}

func (s *unsupportedSession) ID() string                          { return "unsupported" }
func (s *unsupportedSession) Get(key string) interface{}          { return nil }
func (s *unsupportedSession) Put(key string, value interface{})   {}
func (s *unsupportedSession) Has(key string) bool                 { return false }
func (s *unsupportedSession) Remove(key string)                   {}
func (s *unsupportedSession) Clear()                              {}
func (s *unsupportedSession) Regenerate() error                   { return nil }
func (s *unsupportedSession) Invalidate() error                   { return nil }
func (s *unsupportedSession) Flash(key string, value interface{}) {}
func (s *unsupportedSession) GetFlash(key string) interface{}     { return nil }
func (s *unsupportedSession) FlushFlash() map[string]interface{}  { return nil }
func (s *unsupportedSession) Save(w http.ResponseWriter) error    { return nil }

// Compile-time check that unsupportedSession implements auth.Session
var _ auth.Session = (*unsupportedSession)(nil)

func TestCookieStore_DestroyedSession_ClearsAllData(t *testing.T) {
	store := newTestCookieStore(testConfig(), &mockEncryptor{})

	session, _ := store.Create("destroy-test")
	session.Put("key", "value")
	session.Flash("flashKey", "flashValue")

	// Destroy the session
	err := session.Invalidate()
	if err != nil {
		t.Fatalf("Invalidate() error = %v", err)
	}

	// Save the destroyed session
	recorder := httptest.NewRecorder()
	err = store.Save(recorder, session)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Check that cookie has MaxAge -1 (deleted)
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected cookie to be set")
	}

	if cookies[0].MaxAge != -1 {
		t.Errorf("expected MaxAge -1 for destroyed session, got %d", cookies[0].MaxAge)
	}

	if cookies[0].Value != "" {
		t.Errorf("expected empty value for destroyed session, got '%s'", cookies[0].Value)
	}
}

func TestCookieStore_Save_WithNilMaps(t *testing.T) {
	// Test that saving a session with nil data/flash maps doesn't panic
	store := newTestCookieStore(testConfig(), &mockEncryptor{})

	// Create a session - the BaseSession initializes with empty maps
	session, _ := store.Create("nil-maps-test")
	session.Put("k", "v") // mark modified so Save persists

	recorder := httptest.NewRecorder()
	err := store.Save(recorder, session)
	if err != nil {
		t.Errorf("Save() with empty maps error = %v", err)
	}

	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Error("expected cookie to be set")
	}
}

func TestCookieStore_GetData_ReturnsCopy(t *testing.T) {
	store := newTestCookieStore(testConfig(), &mockEncryptor{})
	session, _ := store.Create("copy-test")

	cookieSession := session.(*CookieSession)
	cookieSession.Put("original", "value")

	// Get data and modify it
	data := cookieSession.GetData()
	data["modified"] = "newValue"

	// Original session should not be affected
	if cookieSession.Has("modified") {
		t.Error("modifying returned data map should not affect session")
	}
}

func TestCookieStore_Concurrent_Access(t *testing.T) {
	store := newTestCookieStore(testConfig(), &mockEncryptor{})
	session, _ := store.Create("concurrent-test")

	done := make(chan bool)

	// Concurrent reads
	for i := 0; i < 10; i++ {
		go func() {
			_ = session.ID()
			_ = session.Get("key")
			_ = session.Has("key")
			done <- true
		}()
	}

	// Concurrent writes
	for i := 0; i < 10; i++ {
		go func(n int) {
			session.Put("key", n)
			session.Flash("flash", n)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 20; i++ {
		<-done
	}

	// Verify session is still in valid state
	if session.ID() != "concurrent-test" {
		t.Error("session ID was corrupted during concurrent access")
	}
}

func TestCookieStore_SessionConfigValues(t *testing.T) {
	configs := []struct {
		name   string
		config auth.SessionConfig
		check  func(t *testing.T, store *CookieStore)
	}{
		{
			name: "stores config correctly",
			config: auth.SessionConfig{
				Name:     "custom_name",
				Lifetime: 240,
				Path:     "/app",
				Domain:   "test.com",
				Secure:   true,
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			},
			check: func(t *testing.T, store *CookieStore) {
				if store.config.Name != "custom_name" {
					t.Errorf("expected Name 'custom_name', got '%s'", store.config.Name)
				}
				if store.config.Lifetime != 240 {
					t.Errorf("expected Lifetime 240, got %d", store.config.Lifetime)
				}
				if store.config.Path != "/app" {
					t.Errorf("expected Path '/app', got '%s'", store.config.Path)
				}
				if store.config.Domain != "test.com" {
					t.Errorf("expected Domain 'test.com', got '%s'", store.config.Domain)
				}
				if !store.config.Secure {
					t.Error("expected Secure to be true")
				}
				if !store.config.HttpOnly {
					t.Error("expected HttpOnly to be true")
				}
				if store.config.SameSite != http.SameSiteStrictMode {
					t.Errorf("expected SameSite Strict, got %v", store.config.SameSite)
				}
			},
		},
	}

	for _, tt := range configs {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestCookieStore(tt.config, &mockEncryptor{})
			tt.check(t, store)
		})
	}
}

func TestCookieStore_EmptySessionID_Generates_Valid_ID(t *testing.T) {
	store := newTestCookieStore(testConfig(), &mockEncryptor{})

	session1, _ := store.Create("")
	session2, _ := store.Create("")

	if session1.ID() == "" {
		t.Error("expected generated ID, got empty string")
	}

	if session2.ID() == "" {
		t.Error("expected generated ID, got empty string")
	}

	// IDs should be unique
	if session1.ID() == session2.ID() {
		t.Error("expected unique IDs for different sessions")
	}
}

func TestCookieStore_Get_WithDifferentCookieName(t *testing.T) {
	config := auth.SessionConfig{
		Name:     "custom_session_name",
		Lifetime: 120,
		Path:     "/",
	}

	encryptor := &mockEncryptor{}
	store := newTestCookieStore(config, encryptor)

	// Create request with cookie using different name
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{
		Name:  "wrong_cookie_name",
		Value: "some-value",
	})

	// Should return new session since cookie name doesn't match
	session, err := store.Get(req, "")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	// Session should have new generated ID (not from cookie)
	if session.ID() == "" {
		t.Error("expected generated session ID")
	}
}

func TestCookieSession_StoreReference(t *testing.T) {
	store := newTestCookieStore(testConfig(), &mockEncryptor{})
	session, _ := store.Create("store-ref-test")

	cookieSession := session.(*CookieSession)

	if cookieSession.store != store {
		t.Error("CookieSession should maintain reference to its store")
	}
}

func TestCookieStore_Save_ExpiresHeader(t *testing.T) {
	store := newTestCookieStore(testConfig(), &mockEncryptor{})
	session, _ := store.Create("expires-test")
	session.Put("k", "v") // mark modified so Save persists

	before := time.Now()
	recorder := httptest.NewRecorder()
	err := store.Save(recorder, session)
	after := time.Now()

	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected cookie to be set")
	}

	// Expires should be approximately Lifetime minutes from now
	expectedExpires := before.Add(time.Duration(testConfig().Lifetime) * time.Minute)
	maxExpires := after.Add(time.Duration(testConfig().Lifetime) * time.Minute)

	if cookies[0].Expires.Before(expectedExpires.Add(-time.Second)) {
		t.Error("Expires time is too early")
	}
	if cookies[0].Expires.After(maxExpires.Add(time.Second)) {
		t.Error("Expires time is too late")
	}
}

func TestCookieStore_Save_JSONSerialization(t *testing.T) {
	var capturedJSON string
	encryptor := &mockEncryptor{
		encryptFunc: func(plaintext string) (string, error) {
			capturedJSON = plaintext
			return plaintext, nil
		},
	}
	store := newTestCookieStore(testConfig(), encryptor)

	session, _ := store.Create("json-test-id")
	session.Put("key", "value")
	session.Flash("flash", "flashvalue")

	recorder := httptest.NewRecorder()
	err := store.Save(recorder, session)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify the JSON structure
	var sessionData struct {
		ID    string                 `json:"id"`
		Data  map[string]interface{} `json:"data"`
		Flash map[string]interface{} `json:"flash"`
	}

	if err := json.Unmarshal([]byte(capturedJSON), &sessionData); err != nil {
		t.Fatalf("failed to parse captured JSON: %v", err)
	}

	if sessionData.ID != "json-test-id" {
		t.Errorf("expected ID 'json-test-id', got '%s'", sessionData.ID)
	}

	if sessionData.Data["key"] != "value" {
		t.Errorf("expected data key 'value', got '%v'", sessionData.Data["key"])
	}

	if sessionData.Flash["flash"] != "flashvalue" {
		t.Errorf("expected flash 'flashvalue', got '%v'", sessionData.Flash["flash"])
	}
}

func TestCookieStore_UsesCorrectEncryptor(t *testing.T) {
	tests := []struct {
		name        string
		encryptor   *mockEncryptor
		expectCalls bool
	}{
		{
			name: "uses instance encryptor when set",
			encryptor: &mockEncryptor{
				encryptFunc: func(plaintext string) (string, error) {
					return "instance-encrypted:" + plaintext, nil
				},
			},
			expectCalls: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestCookieStore(testConfig(), tt.encryptor)
			session, _ := store.Create("encryptor-test")
			session.Put("k", "v") // mark modified so Save persists

			recorder := httptest.NewRecorder()
			err := store.Save(recorder, session)
			if err != nil {
				t.Fatalf("Save() error = %v", err)
			}

			cookies := recorder.Result().Cookies()
			if len(cookies) == 0 {
				t.Fatal("expected cookie to be set")
			}

			// Check that the value starts with our mock's prefix
			if tt.expectCalls && len(cookies[0].Value) > 18 {
				if cookies[0].Value[:18] != "instance-encrypted" {
					t.Errorf("expected value to use instance encryptor, got '%s'", cookies[0].Value[:18])
				}
			}
		})
	}
}

func TestCookieStore_BaseSession_WrapperBehavior(t *testing.T) {
	store := newTestCookieStore(testConfig(), &mockEncryptor{})

	// Create a BaseSession directly
	baseSession := auth.NewSession("base-session-id")
	baseSession.Put("key", "value")

	recorder := httptest.NewRecorder()
	err := store.Save(recorder, baseSession)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected cookie to be set")
	}

	// Cookie should be set with the session data
	if cookies[0].Name != testConfig().Name {
		t.Errorf("expected cookie name '%s', got '%s'", testConfig().Name, cookies[0].Name)
	}
}

func TestCookieStore_Clear_Session(t *testing.T) {
	encryptor := &mockEncryptor{
		encryptFunc: func(plaintext string) (string, error) {
			// Use base64 encoding to make the value cookie-safe
			return base64.URLEncoding.EncodeToString([]byte(plaintext)), nil
		},
		decryptFunc: func(payload string) (string, error) {
			decoded, err := base64.URLEncoding.DecodeString(payload)
			if err != nil {
				return payload, nil
			}
			return string(decoded), nil
		},
	}
	store := newTestCookieStore(testConfig(), encryptor)

	// Create session with data
	session, _ := store.Create("clear-test")
	session.Put("key1", "value1")
	session.Put("key2", "value2")

	// Clear the session
	session.Clear()

	// Save and restore
	recorder := httptest.NewRecorder()
	_ = store.Save(recorder, session)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(recorder.Result().Cookies()[0])

	restored, _ := store.Get(req, "")

	// Data should be cleared
	if restored.Get("key1") != nil {
		t.Error("expected key1 to be nil after clear")
	}
	if restored.Get("key2") != nil {
		t.Error("expected key2 to be nil after clear")
	}
}

func TestCookieStore_Types_Implement_Interfaces(t *testing.T) {
	// Compile-time interface checks
	var _ auth.SessionStore = (*CookieStore)(nil)
	var _ auth.Session = (*CookieSession)(nil)

	// Additional runtime check
	store := newTestCookieStore(testConfig(), &mockEncryptor{})
	storeType := reflect.TypeOf(store)
	sessionStoreType := reflect.TypeOf((*auth.SessionStore)(nil)).Elem()

	if !storeType.Implements(sessionStoreType) {
		t.Error("CookieStore does not implement auth.SessionStore")
	}

	session, _ := store.Create("interface-check")
	sessionType := reflect.TypeOf(session)
	authSessionType := reflect.TypeOf((*auth.Session)(nil)).Elem()

	if !sessionType.Implements(authSessionType) {
		t.Error("CookieSession does not implement auth.Session")
	}
}

// TestCookieStore_Save_SecondCallNoOpWhenUnchanged pins the contract
// that "save only on modification" must hold across repeated Save()
// calls on the same session. The first Save() persists the mutation
// and clears the modified flag; the second Save() must NOT emit a new
// Set-Cookie. Without this, every Encrypt() produces a fresh IV and
// rotates the cookie on every response, breaking anything keyed by
// the ciphertext (e.g. the CSRF token store).
func TestCookieStore_Save_SecondCallNoOpWhenUnchanged(t *testing.T) {
	store := newTestCookieStore(testConfig(), &mockEncryptor{})
	session, _ := store.Create("stable-id")
	session.Put("k", "v")

	rec1 := httptest.NewRecorder()
	if err := store.Save(rec1, session); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}
	if len(rec1.Result().Cookies()) == 0 {
		t.Fatal("first Save() must emit a cookie")
	}

	// Second Save() on the unchanged session must be a no-op.
	rec2 := httptest.NewRecorder()
	if err := store.Save(rec2, session); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}
	if got := len(rec2.Result().Cookies()); got != 0 {
		t.Fatalf("second Save() must NOT emit a cookie, got %d", got)
	}

	// Mutating again must re-arm the modified flag.
	session.Put("k", "v2")
	rec3 := httptest.NewRecorder()
	if err := store.Save(rec3, session); err != nil {
		t.Fatalf("third Save() error = %v", err)
	}
	if len(rec3.Result().Cookies()) == 0 {
		t.Fatal("third Save() after mutation must emit a cookie")
	}
}
