package schemes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/router"
)

// orderRotator records, for each XSRF-TOKEN write, how many session
// Set-Cookie headers the response already carried.
type orderRotator struct {
	fakeCSRFRotator
	sessionCookiesAtWrite []int
}

func (o *orderRotator) WriteXSRFCookie(_ context.Context, w http.ResponseWriter, sessionID string) {
	n := 0
	for _, raw := range w.Header().Values("Set-Cookie") {
		if strings.HasPrefix(raw, "vel_session=") {
			n++
		}
	}
	o.sessionCookiesAtWrite = append(o.sessionCookiesAtWrite, n)
	o.fakeCSRFRotator.WriteXSRFCookie(context.Background(), w, sessionID)
}

// Inside SessionMiddleware, Login only changes the session: the seam saves
// it once and writes the XSRF-TOKEN cookie after that save, or drops the
// cookie when the save failed, so the client never holds a token bound to
// a session id that was not persisted.
func TestSessionMiddleware_LoginXSRFCookieFollowsTheSessionSave(t *testing.T) {
	tests := []struct {
		name           string
		saveErr        error
		wantSessionSet int
		wantXSRFWrites []int
	}{
		{name: "save succeeds", wantSessionSet: 1, wantXSRFWrites: []int{1}},
		{name: "save fails", saveErr: errors.New("cookie too large"), wantSessionSet: 0, wantXSRFWrites: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.saveErr != nil {
				orig := saveSessionFromMiddleware
				saveSessionFromMiddleware = func(*SessionScheme, http.ResponseWriter, auth.Session) error { return tt.saveErr }
				t.Cleanup(func() { saveSessionFromMiddleware = orig })
			}
			scheme := newRealCookieScheme(t)
			rotator := &orderRotator{}
			scheme.SetCSRFTokenRotator(rotator)

			r := router.New()
			r.Use(scheme.SessionMiddleware())
			r.Post("/login", func(c *router.Context) error {
				if err := scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}); err != nil {
					return err
				}
				if got := len(c.Response.Header().Values("Set-Cookie")); got != 0 {
					t.Errorf("Login wrote %d Set-Cookie headers itself, want 0", got)
				}
				return c.Redirect(http.StatusSeeOther, "/home")
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/login", nil))

			n := 0
			for _, raw := range w.Header().Values("Set-Cookie") {
				if strings.HasPrefix(raw, "vel_session=") {
					n++
				}
			}
			if n != tt.wantSessionSet {
				t.Errorf("session Set-Cookie headers = %d, want %d", n, tt.wantSessionSet)
			}
			if len(rotator.sessionCookiesAtWrite) != len(tt.wantXSRFWrites) {
				t.Fatalf("XSRF writes = %v, want %v", rotator.sessionCookiesAtWrite, tt.wantXSRFWrites)
			}
			for i, got := range rotator.sessionCookiesAtWrite {
				if got != tt.wantXSRFWrites[i] {
					t.Errorf("XSRF write %d saw %d session cookies, want %d", i, got, tt.wantXSRFWrites[i])
				}
			}
		})
	}
}
