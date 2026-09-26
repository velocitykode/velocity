package velocity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/router"
)

// The session flash bag and the intended-redirect resolver act only on a
// session that is saved when the request ends. A holder WithSessionContext
// attached on its own (no session middleware) caches a session nothing
// saves: flash written there would be lost and a one-shot intended URL
// removed there would come back, so both answer as if the request carried
// no session.
func TestSessionAccessors_IgnoreASessionNothingSaves(t *testing.T) {
	for k, v := range map[string]string{
		"APP_ENV":      "local",
		"APP_KEY":      strings.Repeat("k", 32),
		"AUTH_SCHEME":  "web",
		"LOG_DRIVER":   "null",
		"CACHE_DRIVER": "memory",
		"QUEUE_DRIVER": "memory",
		"MAIL_DRIVER":  "log",
	} {
		t.Setenv(k, v)
	}
	a, err := New(WithConfig(ConfigFromEnv()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	m := auth.FromServices(a.Services)
	scheme, err := m.DefaultScheme()
	if err != nil {
		t.Fatalf("DefaultScheme: %v", err)
	}
	sessions := scheme.(*schemes.SessionScheme)

	var intended string
	var flashBagHeld bool
	a.Router.Get("/unsaved", func(c *router.Context) error {
		c.Request = schemes.WithSessionContext(c.Request)
		sess := sessions.Session(c.Request)
		if sess == nil {
			t.Error("premise: the holder caches no session")
			return nil
		}
		sess.Put(router.IntendedSessionKey, "/from-an-unsaved-session")
		flashBagHeld = a.Services.FlashBag(c.Request) != nil
		intended = c.Intended("/home")
		return c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	a.Router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/unsaved", nil))
	if flashBagHeld {
		t.Error("the flash bag answered for a session nothing saves")
	}
	if intended != "/home" {
		t.Errorf("Intended = %q from a session nothing saves, want the fallback /home", intended)
	}
}
