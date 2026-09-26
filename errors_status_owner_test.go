package velocity

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validation"
)

// TestFrameworkRenderRules_AnswerOnlyTheStatusOwner drives the auth,
// validation and csrf default render rules through a real app. Each rule
// answers its subsystem error when that error owns the response status. A
// panic carrying the error is a reported 500, and a map rule that gives
// the error another status (keeping it as the cause) renders that status:
// neither reaches the subsystem default.
func TestFrameworkRenderRules_AnswerOnlyTheStatusOwner(t *testing.T) {
	customCSRF := func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("custom csrf page"))
	}
	tests := []struct {
		name      string
		subsystem string // auth, validation or csrf
		mode      string // return, panic or map500

		wantStatus   int
		wantLocation string
		wantFlash    bool
		wantBody     string
		wantReports  int
	}{
		{name: "auth returned", subsystem: "auth", mode: "return", wantStatus: http.StatusSeeOther, wantLocation: "/auth/sign-in"},
		{name: "auth panic", subsystem: "auth", mode: "panic", wantStatus: http.StatusInternalServerError, wantReports: 1},
		{name: "auth mapped to 500", subsystem: "auth", mode: "map500", wantStatus: http.StatusInternalServerError, wantReports: 1},
		{name: "validation returned", subsystem: "validation", mode: "return", wantStatus: http.StatusSeeOther, wantLocation: "/signup", wantFlash: true},
		{name: "validation panic", subsystem: "validation", mode: "panic", wantStatus: http.StatusInternalServerError, wantReports: 1},
		{name: "validation mapped to 500", subsystem: "validation", mode: "map500", wantStatus: http.StatusInternalServerError, wantReports: 1},
		{name: "csrf returned", subsystem: "csrf", mode: "return", wantStatus: http.StatusTeapot, wantBody: "custom csrf page"},
		{name: "csrf panic", subsystem: "csrf", mode: "panic", wantStatus: http.StatusInternalServerError, wantReports: 1},
		{name: "csrf mapped to 500", subsystem: "csrf", mode: "map500", wantStatus: http.StatusInternalServerError, wantReports: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _, rec, bag := validationApp(t, backToSignup{})
			h, ok := a.Services.Errors.(*problem.Handler)
			if !ok {
				t.Fatalf("Services.Errors is %T, want *problem.Handler", a.Services.Errors)
			}
			// fail returns err, or panics with it in panic mode.
			fail := func(err error) error {
				if tt.mode == "panic" {
					panic(err)
				}
				return err
			}

			var req *http.Request
			switch tt.subsystem {
			case "auth":
				if tt.mode == "map500" {
					problem.MapFor(h, func(err *auth.UnauthenticatedError) error { return problem.Internal().WithCause(err) })
				}
				a.Router.Get("/account", func(*router.Context) error {
					return fail(&auth.UnauthenticatedError{RedirectTo: "/auth/sign-in"})
				})
				req = httptest.NewRequest(http.MethodGet, "/account", nil)
			case "validation":
				if tt.mode == "map500" {
					problem.MapFor(h, func(err *validation.Failure) error { return problem.Internal().WithCause(err) })
				}
				failure := signupFailure(t)
				a.Router.Post("/signup", func(*router.Context) error { return fail(failure) })
				req = httptest.NewRequest(http.MethodPost, "/signup", strings.NewReader(invalidSignup))
			case "csrf":
				if tt.mode == "map500" {
					problem.MapFor(h, func(err *csrf.TokenMismatchError) error { return problem.Internal().WithCause(err) })
				}
				c := newEnforcingCSRF(t, customCSRF, "")
				if err := c.RotateToken("", "s1"); err != nil {
					t.Fatalf("RotateToken: %v", err)
				}
				other, err := csrf.GenerateToken()
				if err != nil {
					t.Fatalf("GenerateToken: %v", err)
				}
				protect := router.CSRFMiddleware(c)
				a.Router.Post("/posts", func(*router.Context) error {
					t.Error("handler must not run on a CSRF rejection")
					return nil
				}).Use(func(next router.HandlerFunc) router.HandlerFunc {
					guarded := protect(next)
					return func(ctx *router.Context) error {
						if err := guarded(ctx); err != nil {
							return fail(err)
						}
						return nil
					}
				})
				req = httptest.NewRequest(http.MethodPost, "/posts", nil)
				req.AddCookie(&http.Cookie{Name: "session_id", Value: "s1"})
				req.Header.Set("X-CSRF-Token", other)
			}
			req.Header.Set("Accept", "text/html")
			w := httptest.NewRecorder()
			a.Router.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			if got := w.Header().Get("Location"); got != tt.wantLocation {
				t.Errorf("Location = %q, want %q", got, tt.wantLocation)
			}
			if flashed := bag.has(router.FlashErrorsKey); flashed != tt.wantFlash {
				t.Errorf("errors flashed = %v, want %v", flashed, tt.wantFlash)
			}
			if tt.wantBody != "" && w.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", w.Body.String(), tt.wantBody)
			}
			if tt.wantBody == "" && strings.Contains(w.Body.String(), "custom csrf page") {
				t.Errorf("csrf ErrorHandler answered: %q", w.Body.String())
			}
			if got := rec.count(); got != tt.wantReports {
				t.Errorf("reports = %d, want %d (%v)", got, tt.wantReports, rec.errs)
			}
		})
	}
}
