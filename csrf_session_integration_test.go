package velocity

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/csrf/stores"
)

// buildAppWiredResolver mirrors the resolver wiring performed by
// (*App).bootstrap so the integration test covers the exact code path
// consumers hit at runtime. Keep this in sync with app.go.
func buildAppWiredResolver(enc crypto.Encryptor, sessionCookieName string) func(*http.Request) (string, error) {
	return func(r *http.Request) (string, error) {
		c, err := r.Cookie(sessionCookieName)
		if err != nil || c.Value == "" {
			return "", csrf.ErrNoSession
		}
		plaintext, err := enc.Decrypt(c.Value)
		if err != nil {
			return "", csrf.ErrNoSession
		}
		var payload struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(plaintext), &payload); err != nil || payload.ID == "" {
			return "", csrf.ErrNoSession
		}
		return payload.ID, nil
	}
}

// TestCSRF_SessionEncryptionRotation_EndToEnd exercises the full bug
// scenario through real crypto + session.CookieStore + csrf.CSRF:
//
//  1. A session is established and saved (cookie A is the ciphertext).
//  2. The session is mutated again and saved (cookie B, fresh IV).
//  3. A CSRF token previously bound to the plaintext id must validate
//     against both cookie A and cookie B.
//
// This is the regression for the pair of bugs that caused 419 on the
// second state-changing request after a session-modifying response.
func TestCSRF_SessionEncryptionRotation_EndToEnd(t *testing.T) {
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	sessCfg := auth.SessionConfig{
		Name:     "velocity_session",
		Lifetime: 60,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	store, err := session.NewCookieStore(sessCfg, enc)
	if err != nil {
		t.Fatalf("NewCookieStore: %v", err)
	}

	// First save: mutate and persist. Capture cookie A.
	sess, err := store.Create("")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	plaintextID := sess.ID()
	sess.Put("user_id", "alice")
	rec := httptest.NewRecorder()
	if err := store.Save(rec, sess); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("first Save did not emit a cookie")
	}
	cookieA := cookies[0].Value

	// Wire CSRF with the same resolver shape used in app.go.
	csrfCfg := csrf.DefaultConfig()
	csrfCfg.Store = stores.NewSessionStore()
	csrfCfg.Secure = false // test env
	csrfCfg.SessionIDResolver = buildAppWiredResolver(enc, sessCfg.Name)
	c := csrf.New(csrfCfg)

	// Seed a token under the plaintext id (simulates a prior GET that
	// minted a token via setTokenHeader).
	token, err := csrf.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if err := csrfCfg.Store.Set(plaintextID, token); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	doRequest := func(cookieValue string) int {
		req := httptest.NewRequest("DELETE", "/servers/x", nil)
		req.Header.Set(csrfCfg.HeaderName, token)
		req.AddCookie(&http.Cookie{Name: sessCfg.Name, Value: cookieValue})
		w := httptest.NewRecorder()
		c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(w, req)
		return w.Code
	}

	// Request 1: state-changing call with cookie A. Must validate.
	if code := doRequest(cookieA); code != http.StatusOK {
		t.Fatalf("request 1 (cookie A): expected 200, got %d", code)
	}

	// Mutate session again and Save: this rotates the cookie to a new
	// ciphertext B (different IV from A).
	sess.Put("k", "v2")
	rec2 := httptest.NewRecorder()
	if err := store.Save(rec2, sess); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	cookies2 := rec2.Result().Cookies()
	if len(cookies2) == 0 {
		t.Fatal("second Save did not emit a cookie")
	}
	cookieB := cookies2[0].Value
	if cookieA == cookieB {
		t.Fatal("cookie did not rotate between Saves; test cannot exercise the regression")
	}

	// Request 2: state-changing call with cookie B. Pre-fix this 419'd
	// because the CSRF store was keyed by the now-stale cookie A.
	if code := doRequest(cookieB); code != http.StatusOK {
		t.Fatalf("request 2 (cookie B after IV rotation): expected 200, got %d", code)
	}
}

// TestCSRF_SessionResolver_NoEphemeralSession is the regression for
// Finding 1 of the post-fix review: if the resolver fell back to
// auth.Manager.Session(r) it would silently create a fresh session
// (via store.Create("")) when no cookie was present and hand a
// random id to CSRF, reintroducing the ephemeral-session attack
// surface that TestCSRF_RefusesEphemeralSession pins.
//
// The app-wired resolver must require a real cookie AND a successful
// decrypt; anything else MUST return ErrNoSession.
func TestCSRF_SessionResolver_NoEphemeralSession(t *testing.T) {
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	resolver := buildAppWiredResolver(enc, "velocity_session")

	cases := []struct {
		name string
		mut  func(r *http.Request)
	}{
		{
			name: "no cookie present",
			mut:  func(r *http.Request) {},
		},
		{
			name: "cookie present but empty",
			mut: func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: "velocity_session", Value: ""})
			},
		},
		{
			name: "cookie present but undecryptable garbage",
			mut: func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: "velocity_session", Value: "not-a-real-payload"})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/x", nil)
			tc.mut(req)
			id, err := resolver(req)
			if err == nil {
				t.Fatalf("expected ErrNoSession, got id=%q nil err", id)
			}
			if !errors.Is(err, csrf.ErrNoSession) {
				t.Fatalf("expected ErrNoSession, got %v", err)
			}
			if id != "" {
				t.Fatalf("expected empty id, got %q", id)
			}
		})
	}
}
