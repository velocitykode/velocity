package view

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/bond"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/problem/routerbridge"
	"github.com/velocitykode/velocity/router"
)

// TestRenderErrorPage_PartialReloadKeepsErrorProps drives a failed Inertia
// partial reload through a real router wired the way the app wires it: the
// routerbridge error boundary, a non-debug problem.Handler whose error page
// renderer is the view engine, and the engine's Inertia middleware.
//
// The client already shows the error page (the partial component is the
// configured ErrorPage) and reloads a subset of its props. The request
// fails, so the answer is a fresh error page at the new status, and it must
// carry the "status" and "message" props the error page promises (see
// Config.ErrorPage and Engine.RenderErrorPage) whatever the partial headers
// name. An Inertia client merges a partial response for the same component
// into the page it shows, so a page object without them would keep the
// previous failure's status and message under the new HTTP status. The
// shared prop the partial reload asks for still arrives: the answer stays
// a partial render.
func TestRenderErrorPage_PartialReloadKeepsErrorProps(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		headers     map[string]string
		wantStatus  int
		wantMessage string
	}{
		{
			// Partial-Data form: the error page reloads only a shared prop.
			name: "PartialDataNamesAnotherProp", path: "/missing",
			headers: map[string]string{
				bond.HeaderPartialComponent: "Error",
				bond.HeaderPartialOnly:      "notificationCount",
			},
			wantStatus: http.StatusNotFound, wantMessage: "Not Found",
		},
		{
			// Partial-Except form: the error page skips the props it holds.
			name: "PartialExceptNamesErrorProps", path: "/broken",
			headers: map[string]string{
				bond.HeaderPartialComponent: "Error",
				bond.HeaderPartialExcept:    "status,message",
			},
			wantStatus: http.StatusInternalServerError, wantMessage: "Internal Server Error",
		},
		{
			// Control: the same partial reload from another component is
			// not filtered against the error page, so both props arrive.
			name: "ControlPartialOfAnotherComponent", path: "/missing",
			headers: map[string]string{
				bond.HeaderPartialComponent: "Dashboard",
				bond.HeaderPartialOnly:      "notificationCount",
			},
			wantStatus: http.StatusNotFound, wantMessage: "Not Found",
		},
		{
			// Control: a plain Inertia visit gets the full error page.
			name: "ControlNoPartialHeaders", path: "/broken",
			wantStatus: http.StatusInternalServerError, wantMessage: "Internal Server Error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, err := NewEngine(Config{Version: "1", ErrorPage: "Error"})
			if err != nil {
				t.Fatalf("NewEngine: %v", err)
			}
			// A shared prop the error page's layout shows and reloads.
			engine.Share("notificationCount", 3)

			h := problem.NewHandler(problem.WithDebug(false), problem.WithReporters())
			h.SetErrorPageRenderer(engine)

			r := router.New()
			routerbridge.Install(r, routerbridge.WithHandler(func() contract.ErrorHandler { return h }))
			r.Use(engine.Middleware())
			r.Get("/missing", func(*router.Context) error { return problem.NotFound() })
			r.Get("/broken", func(*router.Context) error { return errors.New("database unreachable") })

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set(bond.HeaderInertia, "true")
			req.Header.Set(bond.HeaderVersion, "1")
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			if got := w.Header().Get(bond.HeaderInertia); got != "true" {
				t.Fatalf("X-Inertia = %q, want true: not a page object (body %q)", got, w.Body.String())
			}
			body := strings.TrimSpace(w.Body.String())
			var page bond.Page
			if err := json.Unmarshal([]byte(body), &page); err != nil {
				t.Fatalf("decode page: %v (%s)", err, body)
			}
			if page.Component != "Error" {
				t.Fatalf("component = %q, want Error (body %s)", page.Component, body)
			}
			if got, ok := page.Props["status"].(float64); !ok || int(got) != tt.wantStatus {
				t.Errorf("props.status = %v, want %d: the error page answering HTTP %d lost its status prop to the partial filter (body %s)",
					page.Props["status"], tt.wantStatus, w.Code, body)
			}
			if got := page.Props["message"]; got != tt.wantMessage {
				t.Errorf("props.message = %v, want %q: the error page lost its message prop to the partial filter (body %s)",
					got, tt.wantMessage, body)
			}
			if got, ok := page.Props["notificationCount"].(float64); !ok || got != 3 {
				t.Errorf("props.notificationCount = %v, want 3 (body %s)", page.Props["notificationCount"], body)
			}
		})
	}
}
