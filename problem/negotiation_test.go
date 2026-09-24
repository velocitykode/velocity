package problem

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

func TestHandler_WantsJSON(t *testing.T) {
	errTeapot := errors.New("teapot")
	tests := []struct {
		name   string
		opts   []Option
		when   func(*http.Request, error) bool
		path   string
		accept string
		err    error
		want   bool
	}{
		{name: "BrowserAccept", path: "/x", accept: "text/html", want: false},
		{name: "JSONAccept", path: "/x", accept: "application/json", want: true},
		{name: "APIMode", opts: []Option{WithAPIMode(true)}, path: "/x", accept: "text/html", want: true},
		{name: "APIPrefix", opts: []Option{WithAPIPrefixes("/api")}, path: "/api/users", accept: "text/html", want: true},
		{name: "OutsideAPIPrefix", opts: []Option{WithAPIPrefixes("/api")}, path: "/users", accept: "text/html", want: false},
		{
			name: "JSONWhenAloneDecidesNo", opts: []Option{WithAPIMode(true)},
			when: func(*http.Request, error) bool { return false },
			path: "/api", accept: "application/json", want: false,
		},
		{
			name: "JSONWhenSeesError",
			when: func(_ *http.Request, err error) bool { return errors.Is(err, errTeapot) },
			path: "/x", accept: "text/html", err: errTeapot, want: true,
		},
		{
			name: "JSONWhenNilError",
			when: func(_ *http.Request, err error) bool { return err == nil },
			path: "/x", accept: "text/html", want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler(tt.opts...)
			if tt.when != nil {
				h.JSONWhen(tt.when)
			}
			r := httptest.NewRequest(http.MethodGet, tt.path, nil)
			r.Header.Set("Accept", tt.accept)
			if got := h.WantsJSON(r, tt.err); got != tt.want {
				t.Errorf("WantsJSON = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRender_RulesSeeNegotiatedJSON asserts Renderable errors and render
// rules read the handler's negotiation answer through rc.WantsJSON.
func TestRender_RulesSeeNegotiatedJSON(t *testing.T) {
	tests := []struct {
		name   string
		opts   []Option
		when   func(*http.Request, error) bool
		path   string
		accept string
		want   bool
	}{
		{name: "BrowserOnAPIPrefix", opts: []Option{WithAPIPrefixes("/api")}, path: "/api/x", accept: "text/html", want: true},
		{name: "BrowserOutsidePrefix", opts: []Option{WithAPIPrefixes("/api")}, path: "/x", accept: "text/html", want: false},
		{name: "JSONClientJSONWhenNo", when: func(*http.Request, error) bool { return false }, path: "/x", accept: "application/json", want: false},
		{name: "APIMode", opts: []Option{WithAPIMode(true)}, path: "/x", accept: "text/html", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler(tt.opts...)
			if tt.when != nil {
				h.JSONWhen(tt.when)
			}
			var seen []bool
			RenderFor(h, func(rc RenderContext, _ *statusErr, _ *ErrorContext) bool {
				seen = append(seen, rc.WantsJSON())
				return false
			})
			h.AddFrameworkRenderRule(contract.RenderRule{
				Match: func(error) bool { return true },
				Render: func(rc RenderContext, _ error, _ *ErrorContext) bool {
					seen = append(seen, rc.WantsJSON())
					return false
				},
			})
			rc, _ := newRC(http.MethodGet, tt.path, "Accept", tt.accept)
			h.HandleRequest(rc, &statusErr{code: http.StatusConflict}, nil)
			if len(seen) != 2 || seen[0] != tt.want || seen[1] != tt.want {
				t.Errorf("rules saw WantsJSON %v, want both %v", seen, tt.want)
			}
		})
	}
}

func TestHandler_RenderJSON(t *testing.T) {
	t.Run("ForcesJSONForBrowser", func(t *testing.T) {
		h, rep, _ := newTestHandler()
		h.BeforeRender(func(rc RenderContext, _ error, status int) int {
			rc.SetHeader("X-Hook", "ran")
			return status
		})
		rc, w := newRC(http.MethodPost, "/signup", "Accept", "text/html", "X-Inertia", "true")
		if !h.RenderJSON(rc, Conflict("taken").WithHeader("X-Extra", "1"), nil) {
			t.Fatal("RenderJSON = false, want true")
		}
		if w.Code != http.StatusConflict || w.Header().Get("Content-Type") != ProblemTypeContent {
			t.Errorf("response = %d %q, want 409 %q", w.Code, w.Header().Get("Content-Type"), ProblemTypeContent)
		}
		if w.Header().Get("X-Hook") != "ran" || w.Header().Get("X-Extra") != "1" {
			t.Errorf("headers = %v, want the hook and error headers", w.Header())
		}
		if !strings.Contains(w.Body.String(), `"detail":"taken"`) {
			t.Errorf("body = %q, want the client message", w.Body.String())
		}
		if rep.count() != 0 {
			t.Errorf("reports = %d, want 0", rep.count())
		}
	})
	t.Run("ConfiguredRendererAndNoRules", func(t *testing.T) {
		h, _, _ := newTestHandler()
		h.AddRenderer("json", jsonStamp{})
		RenderStatus[*statusErr](h, http.StatusTeapot)
		rc, w := newRC(http.MethodGet, "/x", "Accept", "text/html")
		if !h.RenderJSON(rc, &statusErr{code: http.StatusConflict}, nil) {
			t.Fatal("RenderJSON = false, want true")
		}
		if w.Code != http.StatusConflict || w.Body.String() != `{"stamp":true}` {
			t.Errorf("response = %d %q, want the configured renderer at 409", w.Code, w.Body.String())
		}
	})
	t.Run("NothingToDo", func(t *testing.T) {
		h, _, _ := newTestHandler()
		rc, w := newRC(http.MethodGet, "/x")
		if h.RenderJSON(rc, nil, nil) || h.RenderJSON(nil, NotFound(), nil) {
			t.Error("RenderJSON with a nil error or render context = true, want false")
		}
		rc.WriteHeader(http.StatusAccepted)
		if h.RenderJSON(rc, NotFound(), nil) {
			t.Error("RenderJSON after a write = true, want false")
		}
		if w.Code != http.StatusAccepted {
			t.Errorf("status = %d, want the first write kept", w.Code)
		}
	})
}

// jsonStamp is a JSON renderer that writes a fixed body.
type jsonStamp struct{}

func (jsonStamp) ContentType() string { return "application/json" }

func (jsonStamp) Render(rc RenderContext, _ error, _ *ErrorContext, status int, _ bool) error {
	rc.SetHeader("Content-Type", "application/json")
	rc.WriteHeader(status)
	_, err := rc.Write([]byte(`{"stamp":true}`))
	return err
}
