package velocity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/csrf/stores"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// newEnforcingCSRF returns a CSRF instance that enforces tokens (no
// testing bypass) and binds them to the raw "session_id" cookie value. A
// non-empty errorMessage replaces Config.ErrorMessage.
func newEnforcingCSRF(t *testing.T, errorHandler func(http.ResponseWriter, *http.Request, error), errorMessage string) *csrf.CSRF {
	t.Helper()
	cfg := csrf.DefaultConfig()
	if errorMessage != "" {
		cfg.ErrorMessage = errorMessage
	}
	cfg.Store = stores.NewMemoryStore()
	cfg.SessionIDResolver = func(r *http.Request) (string, error) {
		ck, err := r.Cookie("session_id")
		if err != nil || ck.Value == "" {
			return "", csrf.ErrNoSession
		}
		return ck.Value, nil
	}
	cfg.ErrorHandler = errorHandler
	c, err := csrf.NewE(cfg)
	if err != nil {
		t.Fatalf("csrf.NewE: %v", err)
	}
	return c
}

// TestErrorPipeline_CSRFRejection drives a rejected CSRF token through a
// real app: router.CSRFMiddleware returns the typed error, the bridge hands
// it to the handler New built, and the framework rules render it.
func TestErrorPipeline_CSRFRejection(t *testing.T) {
	customHandler := func(w http.ResponseWriter, _ *http.Request, err error) {
		w.Header().Set("X-Reason", err.Error())
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("custom csrf page"))
	}
	tests := []struct {
		name         string
		headers      map[string]string
		errorHandler func(http.ResponseWriter, *http.Request, error)
		errorMessage string
		configure    func(h contract.ErrorHandler)

		wantStatus      int
		wantContentType string
		wantHeader      map[string]string
		wantBody        string
		wantDetail      string // non-empty: expect a problem+json body with this detail
	}{
		{
			name:            "JSON client gets 419 problem+json",
			headers:         map[string]string{"Accept": "application/json"},
			wantStatus:      contract.StatusTokenMismatch,
			wantContentType: problem.ProblemTypeContent,
			wantDetail:      csrf.DefaultConfig().ErrorMessage,
		},
		{
			name:            "configured ErrorMessage is the detail",
			headers:         map[string]string{"Accept": "application/json"},
			errorMessage:    "Your session expired, please retry.",
			wantStatus:      contract.StatusTokenMismatch,
			wantContentType: problem.ProblemTypeContent,
			wantDetail:      "Your session expired, please retry.",
		},
		{
			name:       "Inertia client gets the 409 location reload",
			headers:    map[string]string{"X-Inertia": "true", "Accept": "text/html, application/xhtml+xml", "Referer": "http://example.com/posts/new?draft=1"},
			wantStatus: http.StatusConflict,
			wantHeader: map[string]string{"X-Inertia-Location": "/posts/new?draft=1"},
		},
		{
			name:            "browser gets the 419 page",
			headers:         map[string]string{"Accept": "text/html"},
			wantStatus:      contract.StatusTokenMismatch,
			wantContentType: "text/html",
		},
		{
			name:         "ErrorHandler wins for a JSON client",
			headers:      map[string]string{"Accept": "application/json"},
			errorHandler: customHandler,
			wantStatus:   http.StatusTeapot,
			wantHeader:   map[string]string{"X-Reason": csrf.ErrTokenInvalid.Error()},
			wantBody:     "custom csrf page",
		},
		{
			name:         "ErrorHandler wins for an Inertia client",
			headers:      map[string]string{"X-Inertia": "true"},
			errorHandler: customHandler,
			wantStatus:   http.StatusTeapot,
			wantHeader:   map[string]string{"X-Inertia-Location": ""},
			wantBody:     "custom csrf page",
		},
		{
			name:         "application render rule outranks the ErrorHandler",
			headers:      map[string]string{"Accept": "application/json"},
			errorHandler: customHandler,
			configure: func(h contract.ErrorHandler) {
				problem.RenderFor(h, func(rc problem.RenderContext, tm *csrf.TokenMismatchError, _ *problem.ErrorContext) bool {
					rc.WriteHeader(http.StatusGone)
					_, _ = rc.Write([]byte("app rule: " + tm.Reason.Error()))
					return true
				})
			},
			wantStatus: http.StatusGone,
			wantBody:   "app rule: " + csrf.ErrTokenInvalid.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, logs, rec := newPipelineApp(t)
			h, ok := a.Services.Errors.(*problem.Handler)
			if !ok {
				t.Fatalf("Services.Errors is %T, want *problem.Handler", a.Services.Errors)
			}
			h.SetDebug(false)
			if tt.configure != nil {
				tt.configure(h)
			}

			c := newEnforcingCSRF(t, tt.errorHandler, tt.errorMessage)
			other, err := csrf.GenerateToken()
			if err != nil {
				t.Fatalf("GenerateToken: %v", err)
			}
			if err := c.RotateToken(context.Background(), "", "s1"); err != nil {
				t.Fatalf("RotateToken: %v", err)
			}
			a.Router.Post("/posts", func(*router.Context) error {
				t.Fatal("handler must not run on a CSRF rejection")
				return nil
			}).Use(router.CSRFMiddleware(c))

			req := httptest.NewRequest(http.MethodPost, "/posts", nil)
			req.AddCookie(&http.Cookie{Name: "session_id", Value: "s1"})
			// A well-formed token that is not the one stored for s1.
			req.Header.Set("X-CSRF-Token", other)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			a.Router.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			if tt.wantContentType != "" && !strings.HasPrefix(w.Header().Get("Content-Type"), tt.wantContentType) {
				t.Errorf("Content-Type = %q, want %q", w.Header().Get("Content-Type"), tt.wantContentType)
			}
			for k, v := range tt.wantHeader {
				if got := w.Header().Get(k); got != v {
					t.Errorf("header %s = %q, want %q", k, got, v)
				}
			}
			if tt.wantBody != "" && w.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", w.Body.String(), tt.wantBody)
			}
			if tt.wantDetail != "" {
				var body struct {
					Status int    `json:"status"`
					Title  string `json:"title"`
					Detail string `json:"detail"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatalf("problem body: %v (%q)", err, w.Body.String())
				}
				if body.Status != contract.StatusTokenMismatch || body.Title != "Page Expired" || body.Detail != tt.wantDetail {
					t.Errorf("problem body = %+v, want status 419, title Page Expired, detail %q", body, tt.wantDetail)
				}
			}
			if got := rec.count(); got != 0 {
				t.Errorf("reports = %d, want 0 (%v)", got, rec.errs)
			}
			if got := logs.count("error"); got != 0 {
				t.Errorf("error log entries = %d, want 0 (%+v)", got, logs.entries)
			}
		})
	}
}

// TestErrorPipeline_CSRFRejectionMatchesSentinel pins that a rejection
// matches the sentinel of its own reason, wrapped or not (a missing token
// matches ErrTokenMissing, an invalid one ErrTokenInvalid and not
// ErrTokenMissing), and that a rejection of either kind, and the bare
// sentinel of either kind, answers 419 through the pipeline and is never
// reported.
func TestErrorPipeline_CSRFRejectionMatchesSentinel(t *testing.T) {
	c := newEnforcingCSRF(t, nil, "")
	if err := c.RotateToken(context.Background(), "", "s1"); err != nil {
		t.Fatalf("RotateToken: %v", err)
	}
	other, err := csrf.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	protect := func(token string) error {
		req := httptest.NewRequest(http.MethodPost, "/posts", nil)
		req.AddCookie(&http.Cookie{Name: "session_id", Value: "s1"})
		if token != "" {
			req.Header.Set("X-CSRF-Token", token)
		}
		_, err := c.Protect(httptest.NewRecorder(), req)
		var tm *csrf.TokenMismatchError
		if !errors.As(err, &tm) {
			t.Fatalf("Protect error = %v, want a *csrf.TokenMismatchError", err)
		}
		return err
	}

	tests := []struct {
		name    string
		err     error
		wantIs  error
		wantNot error
	}{
		{name: "MissingToken", err: protect(""), wantIs: csrf.ErrTokenMissing, wantNot: csrf.ErrTokenInvalid},
		{name: "InvalidToken", err: protect(other), wantIs: csrf.ErrTokenInvalid, wantNot: csrf.ErrTokenMissing},
		{name: "BareMissingSentinel", err: csrf.ErrTokenMissing, wantIs: csrf.ErrTokenMissing, wantNot: csrf.ErrTokenInvalid},
		{name: "BareInvalidSentinel", err: csrf.ErrTokenInvalid, wantIs: csrf.ErrTokenInvalid, wantNot: csrf.ErrTokenMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := problem.NewHandler(problem.WithReporters())
			installFrameworkErrorRules(h)
			for _, e := range []error{tt.err, errors.Join(errors.New("context"), tt.err)} {
				if !errors.Is(e, tt.wantIs) || errors.Is(e, tt.wantNot) {
					t.Errorf("errors.Is(%v): %v = %v, %v = %v; want true, false", e, tt.wantIs, errors.Is(e, tt.wantIs), tt.wantNot, errors.Is(e, tt.wantNot))
				}
				if h.ShouldReport(e) {
					t.Errorf("ShouldReport(%v) = true, want false", e)
				}
				req := httptest.NewRequest(http.MethodPost, "/posts", nil)
				req.Header.Set("Accept", "application/json")
				w := httptest.NewRecorder()
				h.HandleRequest(contract.NewRenderContext(w, req), e, nil)
				if w.Code != contract.StatusTokenMismatch {
					t.Errorf("status for %v = %d, want 419", e, w.Code)
				}
			}
		})
	}
}

// TestErrorPipeline_CSRFMapIsInvalidToken asserts an application MapIs
// rule keyed on csrf.ErrTokenInvalid fires for a Protect rejection with
// that reason, through a real app.
func TestErrorPipeline_CSRFMapIsInvalidToken(t *testing.T) {
	a, _, _ := newPipelineApp(t)
	h, ok := a.Services.Errors.(*problem.Handler)
	if !ok {
		t.Fatalf("Services.Errors is %T, want *problem.Handler", a.Services.Errors)
	}
	problem.MapIs(h, csrf.ErrTokenInvalid, func(err error) error {
		return contract.NewHTTPError(http.StatusGone).WithCause(err)
	})

	c := newEnforcingCSRF(t, nil, "")
	if err := c.RotateToken(context.Background(), "", "s1"); err != nil {
		t.Fatalf("RotateToken: %v", err)
	}
	other, err := csrf.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	a.Router.Post("/posts", func(*router.Context) error {
		t.Fatal("handler must not run on a CSRF rejection")
		return nil
	}).Use(router.CSRFMiddleware(c))

	req := httptest.NewRequest(http.MethodPost, "/posts", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: "s1"})
	req.Header.Set("X-CSRF-Token", other)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	a.Router.ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Errorf("status = %d, want 410 from the MapIs rule (body %q)", w.Code, w.Body.String())
	}
}
