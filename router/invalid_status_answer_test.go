package router_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/problem/routerbridge"
	"github.com/velocitykode/velocity/router"
)

// invalidStatusObserved collects what one request left behind: the
// router's RequestFailed and RequestHandled events and, when the problem
// pipeline is installed, whether the render context the error handler was
// handed already counted as written and what the reporters received.
type invalidStatusObserved struct {
	mu       sync.Mutex
	failed   []bool // RequestFailed.Recovered, one entry per event
	handled  []int  // RequestHandled.StatusCode, one entry per event
	written  []bool // rc.Written() when the error handler got the failure
	reported []bool // ErrorContext.Recovered, one entry per report
}

// writtenSpyHandler is the error handler the bridge resolves: it notes
// whether the render context already counts as written, then hands the
// failure to the real problem.Handler unchanged.
type writtenSpyHandler struct {
	contract.ErrorHandler
	obs *invalidStatusObserved
}

func (s writtenSpyHandler) HandleRequest(rc contract.RenderContext, err error, ctx *contract.ErrorContext) {
	s.obs.mu.Lock()
	s.obs.written = append(s.obs.written, rc.Written())
	s.obs.mu.Unlock()
	s.ErrorHandler.HandleRequest(rc, err, ctx)
}

// TestServeHTTP_InvalidStatusAnswers500 asserts a status write net/http
// rejects (a status outside 100-999: net/http panics with "invalid
// WriteHeader code" before committing anything) is answered like any other
// recovered panic: the client receives 500, on the router's default
// boundary and through the problem pipeline the app installs with
// routerbridge. Nothing reached the client, so the boundary does not treat
// the response as committed, and RequestHandled records the status that
// was actually sent.
func TestServeHTTP_InvalidStatusAnswers500(t *testing.T) {
	triggers := []struct {
		name    string
		handler router.HandlerFunc
	}{
		{name: "WriteHeader(99)", handler: func(c *router.Context) error {
			c.Response.WriteHeader(99)
			return nil
		}},
		// The realistic trigger: a zero-value status handed to a helper that
		// passes the caller's status straight through.
		{name: "JSON(0)", handler: func(c *router.Context) error {
			return c.JSON(0, map[string]string{"ok": "yes"})
		}},
		{name: "Status(1000)", handler: func(c *router.Context) error {
			return c.Status(1000)
		}},
	}
	boundaries := []struct {
		name    string
		problem bool // install the problem pipeline the way the app does
	}{
		{name: "default boundary"},
		{name: "problem pipeline", problem: true},
	}
	for _, b := range boundaries {
		for _, tt := range triggers {
			t.Run(b.name+"/"+tt.name, func(t *testing.T) {
				obs := &invalidStatusObserved{}
				r := router.New()
				r.SetEventDispatcher(func(_ context.Context, event interface{}) error {
					obs.mu.Lock()
					defer obs.mu.Unlock()
					switch e := event.(type) {
					case *router.RequestFailed:
						obs.failed = append(obs.failed, e.Recovered)
					case *router.RequestHandled:
						obs.handled = append(obs.handled, e.StatusCode)
					}
					return nil
				})
				if b.problem {
					h := problem.NewHandler(problem.WithReporters(problem.NewCallbackReporter(func(_ error, ec *contract.ErrorContext) {
						obs.mu.Lock()
						obs.reported = append(obs.reported, ec.Recovered)
						obs.mu.Unlock()
					})))
					routerbridge.Install(r, routerbridge.WithHandler(func() contract.ErrorHandler {
						return writtenSpyHandler{ErrorHandler: h, obs: obs}
					}))
				}
				r.Get("/x", tt.handler)
				srv := httptest.NewServer(r)
				defer srv.Close()

				resp, err := srv.Client().Get(srv.URL + "/x")
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if len(body) > 40 {
					body = append(body[:40:40], "..."...)
				}
				// Close waits for the handler to return, so every event
				// and report of the request is recorded before it is read.
				srv.Close()

				obs.mu.Lock()
				defer obs.mu.Unlock()
				t.Logf("client got %d, Content-Length %d, Content-Type %q, body %q; RequestFailed recovered %v; RequestHandled status %v; handler rc.Written %v; reports recovered %v",
					resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), body, obs.failed, obs.handled, obs.written, obs.reported)

				if !slices.Equal(obs.failed, []bool{true}) {
					t.Errorf("RequestFailed recovered = %v, want [true]", obs.failed)
				}
				if b.problem && !slices.Equal(obs.reported, []bool{true}) {
					t.Errorf("problem reports recovered = %v, want [true]", obs.reported)
				}
				if resp.StatusCode != http.StatusInternalServerError {
					t.Errorf("client status = %d, want 500: the recovered panic's answer was suppressed and net/http sent an implicit %d", resp.StatusCode, resp.StatusCode)
				}
				if b.problem && !slices.Equal(obs.written, []bool{false}) {
					t.Errorf("render context handed to the error handler: Written = %v, want [false] (net/http rejected the status, nothing was committed)", obs.written)
				}
				if !slices.Equal(obs.handled, []int{http.StatusInternalServerError}) {
					t.Errorf("RequestHandled status = %v, want [500]: it recorded the rejected status, not what the client got (%d)", obs.handled, resp.StatusCode)
				}
			})
		}
	}
}
