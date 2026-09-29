package schemes

import (
	"context"
	"net/http"
	"testing"

	"github.com/velocitykode/velocity/router"
)

// A Logout followed by a Login in one request leaves the client signed in
// on a new session, with the new remember cookie: the Login starts from a
// fresh session instead of the one the Logout ended. The response carries
// one line per credential cookie, the new values; the old session's cookie
// and record no longer sign in.
func TestSessionScheme_LogoutThenLoginInOneRequestSignsIn(t *testing.T) {
	for _, mode := range lifetimeModes {
		t.Run(mode.name, func(t *testing.T) {
			installLifetimeClock(t)
			scheme, mem := newLifetimeSchemeFor(t, 120, 0, mode)
			b := newRememberBrowser(t, scheme)
			b.do(http.MethodPost, "/login")
			oldSession := *b.cookies[sessionCookieName]

			var oldID, newID string
			var logoutErr, loginErr error
			r := router.New()
			r.Use(scheme.SessionMiddleware())
			r.Post("/switch", func(c *router.Context) error {
				oldID = scheme.Session(c.Request).ID()
				logoutErr = scheme.Logout(c.Response, c.Request)
				loginErr = scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}, true)
				newID = scheme.Session(c.Request).ID()
				return c.String(http.StatusOK, "switched")
			})
			r.Get("/check", func(c *router.Context) error {
				if ok, _ := scheme.CheckWithError(c.Request); !ok {
					return c.String(http.StatusUnauthorized, "out")
				}
				return c.String(http.StatusOK, "in")
			})
			b.handler = r
			w := b.do(http.MethodPost, "/switch")

			if logoutErr != nil || loginErr != nil {
				t.Fatalf("Logout = %v, Login = %v; want both nil", logoutErr, loginErr)
			}
			if newID == "" || newID == oldID {
				t.Fatalf("Login did not move the request to a new session (old %q, new %q)", oldID, newID)
			}
			for _, name := range []string{sessionCookieName, rememberCookieName} {
				var lines []*http.Cookie
				for _, c := range w.Result().Cookies() {
					if c.Name == name {
						lines = append(lines, c)
					}
				}
				if len(lines) != 1 || lines[0].MaxAge < 0 || lines[0].Value == "" {
					t.Errorf("%s lines in the response = %+v, want exactly one, the new value", name, lines)
				}
			}
			if !b.signedIn() {
				t.Fatal("the client is signed out after Logout then Login in one request")
			}
			if mem != nil && oldID != "" {
				if _, err := mem.Get(context.Background(), oldID); err == nil {
					t.Error("the old session's record survived the logout")
				}
			}

			// The new remember cookie recalls the user on its own.
			rem := *b.cookies[rememberCookieName]
			b.replayRememberOnly(rem)
			if !b.signedIn() {
				t.Error("the new remember cookie does not sign in on its own")
			}
			// The old session cookie does not.
			b.cookies = map[string]*http.Cookie{sessionCookieName: &oldSession}
			if b.signedIn() {
				t.Error("the old session cookie still signs in")
			}
		})
	}
}
