package velocity

import (
	"net/http"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/router"
)

// The XSRF-TOKEN cookie a safe request bootstraps names the token kept in
// the request's session, so it is written only once that session is
// saved: when the save fails (here the cookie session outgrows the 4096
// byte cookie limit) the response carries neither the session cookie nor
// an XSRF-TOKEN for a token nobody holds. A request whose session saves
// still gets the cookie.
func TestCSRFBootstrapCookie_FollowsTheSessionSave(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		wantXSRF bool
	}{
		{name: "session saved", path: "/form", wantXSRF: true},
		{name: "session save fails", path: "/big", wantXSRF: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := csrfBagInstance(t, "cookie", nil, false)
			a.Router.Get("/big", func(c *router.Context) error {
				schemes.SessionFromRequest(c.Request).Put("draft", strings.Repeat("x", 5000))
				return c.String(http.StatusOK, "ok")
			})
			w := csrfBagJar{}.send(t, a.Router, http.MethodGet, tt.path, "")
			var session, xsrf bool
			for _, c := range w.Result().Cookies() {
				switch c.Name {
				case a.config.Session.Name:
					session = true
				case "XSRF-TOKEN":
					xsrf = true
				}
			}
			if session != tt.wantXSRF {
				t.Fatalf("session cookie written = %v, want %v", session, tt.wantXSRF)
			}
			if xsrf != tt.wantXSRF {
				t.Fatalf("XSRF-TOKEN written = %v, want %v (session saved = %v)", xsrf, tt.wantXSRF, session)
			}
		})
	}
}
