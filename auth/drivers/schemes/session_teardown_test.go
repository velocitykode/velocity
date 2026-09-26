package schemes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/router"
)

// failingDeleteRecords is a server session store whose Delete fails while
// failing is set; every other call reaches the wrapped store.
type failingDeleteRecords struct {
	auth.ServerSessionStore
	failing atomic.Bool
}

var errRecordDeleteDown = errors.New("session backend down")

func (s *failingDeleteRecords) Delete(ctx context.Context, id string) error {
	if s.failing.Load() {
		return errRecordDeleteDown
	}
	return s.ServerSessionStore.Delete(ctx, id)
}

// teardownConfig is the session configuration of the teardown tests.
func teardownConfig() auth.SessionConfig {
	return auth.SessionConfig{Name: "vel_session", IdleLifetime: 60, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

func teardownEncryptor(t *testing.T) crypto.Encryptor {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{Key: strings.Repeat("k", 32), Cipher: "AES-256-GCM"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	return enc
}

// withForgetRoute adds a route that deletes the session cookie the way a
// handler does (Context.DeleteCookie).
func withForgetRoute(b *storeBrowser) {
	r := b.handler.(*router.VelocityRouterV2)
	r.Get("/forget", func(c *router.Context) error {
		c.DeleteCookie("vel_session")
		return c.String(http.StatusOK, "forgotten")
	})
}

// Ending a session whose server record cannot be removed still deletes
// the session cookie in the browser, and the failed removal is reported:
// the teardown that failed is the server's, not the browser's.
func TestSessionMiddleware_EndedSessionDeletesTheCookieWhenTheRecordStays(t *testing.T) {
	for _, path := range []string{"/forget", "/logout"} {
		t.Run(path, func(t *testing.T) {
			cfg := teardownConfig()
			records := &failingDeleteRecords{ServerSessionStore: session.NewMemoryStore()}
			store, err := session.NewServerStore(cfg, records)
			if err != nil {
				t.Fatalf("NewServerStore: %v", err)
			}
			users := &revokeTestStore{users: map[string]*revokeTestUser{"u1": {id: "u1"}}}
			scheme, err := NewSessionScheme(users, cfg, teardownEncryptor(t), WithSessionStore(store))
			if err != nil {
				t.Fatalf("NewSessionScheme: %v", err)
			}
			logs := &warnLog{}
			scheme.SetLogger(logs)
			b := newStoreBrowser(t, scheme)
			withForgetRoute(b)
			b.do(http.MethodPost, "/login")
			if _, ok := b.cookies["vel_session"]; !ok {
				t.Fatal("premise: no session cookie after sign-in")
			}

			records.failing.Store(true)
			method := http.MethodGet
			if path == "/logout" {
				method = http.MethodPost
			}
			w := b.do(method, path)
			lines := sessionLines(w)
			if len(lines) == 0 {
				t.Fatalf("the response carries no session cookie deletion after the record removal failed: %v", w.Result().Header.Values("Set-Cookie"))
			}
			last, err := http.ParseSetCookie(lines[len(lines)-1])
			if err != nil {
				t.Fatalf("parse %q: %v", lines[len(lines)-1], err)
			}
			if last.MaxAge >= 0 {
				t.Fatalf("the last session cookie line is not a deletion: %v", lines)
			}
			if last.Path != "/" || !last.HttpOnly || last.SameSite != http.SameSiteLaxMode {
				t.Fatalf("the deletion does not carry the session cookie policy: %q", lines[len(lines)-1])
			}
			if _, ok := b.cookies["vel_session"]; ok {
				t.Fatal("the browser still holds the session cookie")
			}
			if !logs.contains("session save failed") {
				t.Fatalf("the failed record removal was not reported: %v", logs.lines)
			}
		})
	}
}

// A handler deleting the session cookie ends the session on every instance
// that shares the session record store, not only on the one that served
// the deletion: a captured copy of the cookie replayed on another instance
// is refused.
func TestSessionMiddleware_DeletedSessionCookieEndsTheSessionOnEveryInstance(t *testing.T) {
	for _, serverSide := range []bool{false, true} {
		name := "cookie store"
		if serverSide {
			name = "server store"
		}
		t.Run(name, func(t *testing.T) {
			cfg := teardownConfig()
			enc := teardownEncryptor(t)
			shared := session.NewMemoryStore()
			users := &revokeTestStore{users: map[string]*revokeTestUser{"u1": {id: "u1"}}}
			instance := func() *SessionScheme {
				var opts []SessionSchemeOption
				if serverSide {
					store, err := session.NewServerStore(cfg, shared)
					if err != nil {
						t.Fatalf("NewServerStore: %v", err)
					}
					opts = append(opts, WithSessionStore(store))
				}
				scheme, err := NewSessionScheme(users, cfg, enc, opts...)
				if err != nil {
					t.Fatalf("NewSessionScheme: %v", err)
				}
				scheme.SetServerSessionStore(shared)
				return scheme
			}
			a := newStoreBrowser(t, instance())
			withForgetRoute(a)
			a.do(http.MethodPost, "/login")
			captured := map[string]*http.Cookie{}
			for k, v := range a.cookies {
				captured[k] = v
			}

			b := newStoreBrowser(t, instance())
			b.cookies = map[string]*http.Cookie{}
			for k, v := range captured {
				b.cookies[k] = v
			}
			if body := b.do(http.MethodGet, "/check").Body.String(); body != "in" {
				t.Fatalf("premise: the second instance does not accept the session: %q (%v)", body, b.lastErr)
			}

			a.do(http.MethodGet, "/forget")
			if _, ok := a.cookies["vel_session"]; ok {
				t.Fatal("premise: the first instance kept the session cookie")
			}

			b.cookies = map[string]*http.Cookie{}
			for k, v := range captured {
				b.cookies[k] = v
			}
			if body := b.do(http.MethodGet, "/check").Body.String(); body == "in" {
				t.Fatal("the session whose cookie was deleted on one instance still signs in on another")
			}
		})
	}
}

// ResolveSession refuses a session the store minted fresh for a request
// whose holder nothing saves, on every call: caching the fresh session on
// the holder does not make it acceptable the second time. Inside a save
// scope the fresh session is the one the response persists and is
// accepted; a session loaded from the cookie is accepted either way.
func TestResolveSession_FreshSessionOutsideASaveScopeStaysRefused(t *testing.T) {
	scheme, _ := storeScheme(t, false)
	b := newStoreBrowser(t, scheme)
	b.do(http.MethodGet, "/public")
	cookie, ok := b.cookies["vel_session"]
	if !ok {
		t.Fatal("premise: no session cookie minted")
	}

	tests := []struct {
		name      string
		cookie    bool
		saveScope bool
		want      error
	}{
		{"fresh, no save scope", false, false, auth.ErrSessionNotFound},
		{"fresh, save scope", false, true, nil},
		{"loaded, no save scope", true, false, nil},
		{"loaded, save scope", true, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.cookie {
				req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
			}
			r := WithSessionContext(req)
			if tt.saveScope {
				r.Context().Value(sessionCtxKey{}).(*sessionHolder).markSaveScope()
			}
			for call := 1; call <= 2; call++ {
				sess, err := scheme.ResolveSession(r)
				if !errors.Is(err, tt.want) || (tt.want == nil && err != nil) {
					t.Fatalf("call %d: ResolveSession error = %v, want %v", call, err, tt.want)
				}
				if tt.want == nil && (sess == nil || sess.ID() == "") {
					t.Fatalf("call %d: accepted no session", call)
				}
			}
		})
	}
}
