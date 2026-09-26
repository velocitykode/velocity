package velocity

import (
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/auth/drivers/session"
)

// TestNew_SessionStoreFromEnv asserts SESSION_STORE picks the session
// scheme's store: the cookie store by default, and with "server" a
// session.ServerStore whose records are the server session store New
// installs on the auth manager, one record per session.
func TestNew_SessionStoreFromEnv(t *testing.T) {
	tests := []struct {
		name       string
		env        string
		wantServer bool
	}{
		{"unset", "", false},
		{"cookie", "cookie", false},
		{"server", "server", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SESSION_STORE", tt.env)
			a, _, _ := sessionFlashServer(t, false)
			mgr := a.Auth.(*auth.Manager)
			def, err := mgr.DefaultScheme()
			if err != nil {
				t.Fatalf("DefaultScheme: %v", err)
			}
			scheme := def.(*schemes.SessionScheme)
			store := scheme.SessionStore()
			if !tt.wantServer {
				if _, ok := store.(*session.CookieStore); !ok {
					t.Fatalf("session store %T, want *session.CookieStore", store)
				}
				if mgr.ServerSessionStore() != nil {
					t.Fatal("the cookie store installed a server session store")
				}
				return
			}
			if _, ok := store.(*session.ServerStore); !ok {
				t.Fatalf("session store %T, want *session.ServerStore", store)
			}
			if _, ok := mgr.ServerSessionStore().(*session.CacheStore); !ok {
				t.Fatalf("server session store %T, want the cache-backed records", mgr.ServerSessionStore())
			}
		})
	}
}

// TestNew_ServerSessionStoreCarriesFlashBehindAnIDCookie runs the starter
// flash flow with SESSION_STORE=server: handlers flash exactly as with the
// cookie store, the next page gets the message, and the session cookie is
// the bare id.
func TestNew_ServerSessionStoreCarriesFlashBehindAnIDCookie(t *testing.T) {
	t.Setenv("SESSION_STORE", "server")
	a, srv, client := sessionFlashServer(t, true)

	resp := sessionFlashPost(t, client, srv.URL, "/save")
	var id string
	for _, c := range resp.Cookies() {
		if c.Name == a.config.Session.Name {
			id = c.Value
		}
	}
	if id == "" || len(id) != 44 {
		t.Fatalf("session cookie value %q, want the 44-character session id", id)
	}
	if _, err := a.Auth.(*auth.Manager).ServerSessionStore().Get(t.Context(), id); err != nil {
		t.Fatalf("no server record for the cookie's id: %v", err)
	}

	_, page := sessionFlashVisit(t, client, srv.URL, false)
	flash, _ := page["flash"].(map[string]any)
	if flash["success"] != "Saved!" {
		t.Fatalf("flash = %v, want success = \"Saved!\"", page["flash"])
	}
	_, page = sessionFlashVisit(t, client, srv.URL, false)
	if page["flash"] != nil {
		t.Fatalf("flash delivered twice: %v", page["flash"])
	}
}

// TestNew_SessionStoreRejectsWhatItCannotRun asserts New fails, with no
// fallback to the cookie store, on an unknown SESSION_STORE and on
// SESSION_STORE=server over a cache that cannot hold session records.
func TestNew_SessionStoreRejectsWhatItCannotRun(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want error
	}{
		{"unknown store", map[string]string{"SESSION_STORE": "redis"}, ErrInvalidConfig},
		{"server over the file cache", map[string]string{"SESSION_STORE": "server", "CACHE_DRIVER": "file", "CACHE_PATH": t.TempDir()}, session.ErrCacheStoreUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range map[string]string{
				"APP_ENV":        "local",
				"APP_KEY":        strings.Repeat("k", 32),
				"AUTH_SCHEME":    "web",
				"LOG_DRIVER":     "null",
				"CACHE_DRIVER":   "memory",
				"QUEUE_DRIVER":   "memory",
				"MAIL_DRIVER":    "log",
				"SESSION_SECURE": "false",
			} {
				t.Setenv(k, v)
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			a, err := New()
			if err == nil {
				_ = a.Shutdown(t.Context())
				t.Fatal("New succeeded")
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("New = %v, want %v", err, tt.want)
			}
		})
	}
}
