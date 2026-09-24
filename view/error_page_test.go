package view

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/bond"
	"github.com/velocitykode/velocity/contract"
)

// failingGateway is an SSR gateway whose every dispatch fails.
type failingGateway struct{ err error }

func (g failingGateway) Dispatch(context.Context, bond.Page) (*bond.SSRResponse, error) {
	return nil, g.err
}

func TestEngine_RenderErrorPage(t *testing.T) {
	tests := []struct {
		name        string
		errorPage   string
		inertia     bool
		status      int
		wantOK      bool
		wantStatus  int
		wantContent string
	}{
		{name: "NoComponent", status: http.StatusNotFound},
		{name: "InertiaXHR", errorPage: "Error", inertia: true, status: http.StatusNotFound, wantOK: true, wantStatus: http.StatusNotFound, wantContent: "application/json"},
		{name: "FullPage", errorPage: "Error", status: http.StatusNotFound, wantOK: true, wantStatus: http.StatusNotFound, wantContent: "text/html; charset=utf-8"},
		{name: "ServerError", errorPage: "Errors/Show", inertia: true, status: http.StatusInternalServerError, wantOK: true, wantStatus: http.StatusInternalServerError, wantContent: "application/json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := NewEngine(Config{ErrorPage: tt.errorPage})
			if err != nil {
				t.Fatalf("NewEngine: %v", err)
			}
			r := httptest.NewRequest(http.MethodGet, "/missing", nil)
			if tt.inertia {
				r.Header.Set("X-Inertia", "true")
			}
			w := httptest.NewRecorder()
			rc := contract.NewRenderContext(w, r)

			ok, err := e.RenderErrorPage(rc, tt.status, "Not Found")
			if err != nil {
				t.Fatalf("RenderErrorPage: %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if rc.Written() != tt.wantOK {
				t.Errorf("rc.Written() = %v, want %v", rc.Written(), tt.wantOK)
			}
			if !tt.wantOK {
				if w.Body.Len() != 0 || len(w.Header()) != 0 {
					t.Errorf("declined page wrote %q with headers %v", w.Body.String(), w.Header())
				}
				return
			}
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if got := w.Header().Get("Content-Type"); got != tt.wantContent {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantContent)
			}
			page := pageFromBody(t, w.Body.String(), tt.inertia)
			if page.Component != tt.errorPage {
				t.Errorf("component = %q, want %q", page.Component, tt.errorPage)
			}
			if got, _ := page.Props["status"].(float64); int(got) != tt.status {
				t.Errorf("props.status = %v, want %d", page.Props["status"], tt.status)
			}
			if page.Props["message"] != "Not Found" {
				t.Errorf("props.message = %v, want Not Found", page.Props["message"])
			}
		})
	}
}

// pageFromBody decodes the page object from a JSON body or from the
// data-page script of an HTML body.
func pageFromBody(t *testing.T, body string, inertia bool) bond.Page {
	t.Helper()
	raw := body
	if !inertia {
		const marker = `type="application/json" data-page="app">`
		start := strings.Index(body, marker)
		if start < 0 {
			t.Fatalf("no page script in body: %s", body)
		}
		raw = body[start+len(marker):]
		raw = raw[:strings.Index(raw, "</script>")]
	}
	var page bond.Page
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatalf("decode page: %v (%s)", err, raw)
	}
	return page
}

func TestEngine_RenderErrorPage_Edges(t *testing.T) {
	e, err := NewEngine(Config{ErrorPage: "Error"})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	ssrErr := errors.New("ssr down")
	tests := []struct {
		name    string
		rc      func() (contract.RenderContext, *httptest.ResponseRecorder)
		prepare func()
		wantOK  bool
		wantErr error
	}{
		{
			name: "NilRenderContext",
			rc:   func() (contract.RenderContext, *httptest.ResponseRecorder) { return nil, httptest.NewRecorder() },
		},
		{
			name: "NilRequest",
			rc: func() (contract.RenderContext, *httptest.ResponseRecorder) {
				w := httptest.NewRecorder()
				return contract.NewRenderContext(w, nil), w
			},
		},
		{
			name: "RenderFailsBeforeWriting",
			rc: func() (contract.RenderContext, *httptest.ResponseRecorder) {
				w := httptest.NewRecorder()
				return contract.NewRenderContext(w, httptest.NewRequest(http.MethodGet, "/", nil)), w
			},
			prepare: func() { e.Bond().SetSSRGateway(failingGateway{err: ssrErr}) },
			wantErr: ssrErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.prepare != nil {
				tt.prepare()
				t.Cleanup(func() { e.Bond().SetSSRGateway(nil) })
			}
			rc, w := tt.rc()
			ok, err := e.RenderErrorPage(rc, http.StatusNotFound, "Not Found")
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if w.Body.Len() != 0 {
				t.Errorf("body written: %q", w.Body.String())
			}
		})
	}
}

func TestEngine_ReloadLocation(t *testing.T) {
	e, err := NewEngine(Config{})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	tests := []struct {
		name    string
		method  string
		target  string
		referer string
		want    string
	}{
		{name: "GETCurrentURL", method: http.MethodGet, target: "/posts?page=2", want: "/posts?page=2"},
		{name: "POSTReferer", method: http.MethodPost, target: "/posts", referer: "/posts/new", want: "/posts/new"},
		{name: "POSTForeignReferer", method: http.MethodPost, target: "/posts", referer: "https://evil.test/x", want: "/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.target, nil)
			if tt.referer != "" {
				r.Header.Set("Referer", tt.referer)
			}
			if got := e.ReloadLocation(r); got != tt.want {
				t.Errorf("ReloadLocation = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderContextWriter(t *testing.T) {
	w := httptest.NewRecorder()
	rc := contract.NewRenderContext(w, httptest.NewRequest(http.MethodGet, "/", nil))
	rw := renderContextWriter{rc: rc}
	rw.Header().Set("X-Test", "1")
	rw.WriteHeader(http.StatusTeapot)
	rw.WriteHeader(http.StatusOK)
	if _, err := rw.Write([]byte("body")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if w.Code != http.StatusTeapot || w.Body.String() != "body" || w.Header().Get("X-Test") != "1" {
		t.Errorf("status %d body %q header %q", w.Code, w.Body.String(), w.Header().Get("X-Test"))
	}
	if !rc.Written() {
		t.Error("RenderContext does not know the response is written")
	}
	if rw.Unwrap() != http.ResponseWriter(w) {
		t.Error("Unwrap did not return the RenderContext's writer")
	}
}
