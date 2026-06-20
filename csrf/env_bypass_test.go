package csrf

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/router"
)

// TestMiddleware_TestingEnvBypass pins the per-instance, config-driven CSRF
// testing bypass and its fail-secure boundary. A CSRF instance whose
// Config.Env names a test profile ("test"/"testing") skips token validation on
// unsafe requests (Laravel PreventRequestForgery.runningUnitTests parity);
// every other Env - including the empty zero value, "development", and
// "production" - enforces.
//
// Keying on Config.Env (set by the app at construction) rather than a
// process-wide os.Getenv is the load-bearing property: csrf's own tests build
// configs that leave Env empty, so they keep exercising real enforcement even
// when the suite runs under `APP_ENV=testing go test ./csrf`.
func TestMiddleware_TestingEnvBypass(t *testing.T) {
	cases := []struct {
		name       string
		env        string
		wantBypass bool
	}{
		{"testing", "testing", true},
		{"test", "test", true},
		{"empty-enforces", "", false},
		{"development-enforces", "development", false},
		{"dev-enforces", "dev", false},
		{"production-enforces", "production", false},
		{"prod-enforces", "prod", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.SessionIDResolver = testCookieResolver("session_id")
			cfg.Store = newCountingStore()
			cfg.Env = tc.env
			c, err := NewE(cfg)
			if err != nil {
				t.Fatalf("NewE: %v", err)
			}

			handlerCalled := false
			rtr := router.New()
			rtr.Use(router.CSRFMiddleware(c))
			rtr.Post("/submit", func(ctx *router.Context) error {
				handlerCalled = true
				ctx.Response.WriteHeader(http.StatusOK)
				return nil
			})

			req := requestWithSession(http.MethodPost, "/submit", "sess-env")
			w := httptest.NewRecorder()
			rtr.ServeHTTP(w, req)

			if tc.wantBypass {
				if w.Code == 419 || !handlerCalled {
					t.Errorf("env=%q: expected token validation to be bypassed (handler reached), got status=%d handlerCalled=%v", tc.env, w.Code, handlerCalled)
				}
			} else {
				if w.Code != 419 || handlerCalled {
					t.Errorf("env=%q: expected 419 enforcement (handler NOT reached), got status=%d handlerCalled=%v", tc.env, w.Code, handlerCalled)
				}
			}
		})
	}
}
