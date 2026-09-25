package routerbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// staticFailureRenderKey keys the counting catch-all render rule.
type staticFailureRenderKey struct{}

// staticFailurePipeline counts each stage of the installed error pipeline a
// failed request passes through: the error handler resolver, a catch-all
// render rule that falls through to the negotiated answer, a BeforeRender
// hook, the reporter and the RequestFailed event.
type staticFailurePipeline struct {
	mu       sync.Mutex
	resolved int
	rendered int
	hooked   []int
	reported int
	failed   int
}

// Report counts one report.
func (p *staticFailurePipeline) Report(error, *contract.ErrorContext) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reported++
}

func (p *staticFailurePipeline) counts() (resolved, rendered int, hooked []int, reported, failed int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resolved, p.rendered, append([]int(nil), p.hooked...), p.reported, p.failed
}

// newStaticFailureRouter wires a router the way an application wires its
// error pipeline: Install with a problem.Handler carrying a render rule, a
// BeforeRender hook and a reporter, plus a RequestFailed listener. dir is
// both the ctx.File root (for the /control route) and the static directory.
func newStaticFailureRouter(dir string, fallback bool, p *staticFailurePipeline) *router.VelocityRouterV2 {
	h := problem.NewHandler(problem.WithReporters(p))
	h.SetDebug(false)
	h.AddRenderRule(contract.RenderRule{
		Key:   staticFailureRenderKey{},
		Match: func(error) bool { return true },
		Render: func(problem.RenderContext, error, *contract.ErrorContext) bool {
			p.mu.Lock()
			p.rendered++
			p.mu.Unlock()
			return false // fall through to the negotiated problem answer
		},
	})
	h.BeforeRender(func(_ problem.RenderContext, _ error, status int) int {
		p.mu.Lock()
		p.hooked = append(p.hooked, status)
		p.mu.Unlock()
		return status
	})

	r := router.New()
	Install(r, WithHandler(func() contract.ErrorHandler {
		p.mu.Lock()
		p.resolved++
		p.mu.Unlock()
		return h
	}))
	r.SetEventDispatcher(func(_ context.Context, ev interface{}) error {
		if _, ok := ev.(*router.RequestFailed); ok {
			p.mu.Lock()
			p.failed++
			p.mu.Unlock()
		}
		return nil
	})
	// The global chain runs after the static probe, so removing the file
	// here makes the probe-then-serve race deterministic.
	r.Use(onPath("/vanish.txt", func(*router.Context) {
		_ = os.Remove(filepath.Join(dir, "vanish.txt"))
	}))
	r.SetFileRoot(dir)
	r.Get("/control", func(c *router.Context) error { return c.File("ok.txt") })
	if fallback {
		r.StaticFallback(dir)
	} else {
		r.Static(dir)
	}
	return r
}

// TestInstall_StaticFileErrorsGoThroughPipeline asserts a failure the
// static file server answers itself (Static and StaticFallback alike)
// reaches the installed error pipeline, the same way the same failure from
// ctx.File does: the handler is resolved, render rules and BeforeRender
// hooks run, the answer is negotiated problem+json at the status net/http
// chose (a 416 keeping its Content-Range), and a 5xx is reported and
// dispatches RequestFailed. The "control" row serves the same unsatisfiable
// Range through ctx.File on the same router and handler. A path the client
// shaped so that no file can answer it (a segment over NAME_MAX, a file
// used as a directory, a NUL byte) is answered 404 through the pipeline,
// by routing after a static miss or by the file server, and nothing is
// reported: a crafted URL never mints a server error.
func TestInstall_StaticFileErrorsGoThroughPipeline(t *testing.T) {
	longName := strings.Repeat("a", 300)

	tests := []struct {
		name         string
		path         string
		header       [2]string // one request header: name, value
		needsUser    bool      // skipped as root, which opens a mode-0 file
		wantStatus   int
		wantRange    string // the answer's Content-Range header
		wantFailures int    // reports and RequestFailed events
	}{
		{name: "control ctx.File unsatisfiable range", path: "/control", header: [2]string{"Range", "bytes=100-200"}, wantStatus: http.StatusRequestedRangeNotSatisfiable, wantRange: "bytes */3"},
		{name: "unsatisfiable range", path: "/ok.txt", header: [2]string{"Range", "bytes=100-200"}, wantStatus: http.StatusRequestedRangeNotSatisfiable, wantRange: "bytes */3"},
		{name: "failed precondition", path: "/ok.txt", header: [2]string{"If-Match", `"nope"`}, wantStatus: http.StatusPreconditionFailed},
		{name: "permission denied", path: "/locked.txt", needsUser: true, wantStatus: http.StatusForbidden},
		{name: "open failure", path: "/loop.txt", wantStatus: http.StatusInternalServerError, wantFailures: 1},
		{name: "file vanished after the probe", path: "/vanish.txt", wantStatus: http.StatusNotFound},
		{name: "segment over NAME_MAX", path: "/" + longName, wantStatus: http.StatusNotFound},
		{name: "file used as a directory", path: "/ok.txt/more", wantStatus: http.StatusNotFound},
		{name: "NUL byte", path: "/ok%00.txt", wantStatus: http.StatusNotFound},
	}
	modes := []struct {
		name     string
		fallback bool
	}{
		{name: "Static"},
		{name: "StaticFallback", fallback: true},
	}
	for _, mode := range modes {
		for _, tt := range tests {
			t.Run(mode.name+"/"+tt.name, func(t *testing.T) {
				if tt.needsUser && os.Geteuid() == 0 {
					t.Skip("root opens a mode-0 file")
				}
				dir := t.TempDir()
				for name, perm := range map[string]os.FileMode{"ok.txt": 0o600, "locked.txt": 0o000, "vanish.txt": 0o600} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte("abc"), perm); err != nil {
						t.Fatal(err)
					}
				}
				// A symlink to itself: the open fails with ELOOP, a
				// server-side fault the file server answers 500.
				if err := os.Symlink("loop.txt", filepath.Join(dir, "loop.txt")); err != nil {
					t.Skipf("symlink: %v", err)
				}
				p := &staticFailurePipeline{}
				r := newStaticFailureRouter(dir, mode.fallback, p)

				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, tt.path, nil)
				req.Header.Set("Accept", "application/json")
				if tt.header[0] != "" {
					req.Header.Set(tt.header[0], tt.header[1])
				}
				r.ServeHTTP(w, req)

				body := w.Body.String()
				if w.Code != tt.wantStatus {
					t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
				}
				if got := w.Header().Get("Content-Range"); got != tt.wantRange {
					t.Errorf("Content-Range = %q, want %q", got, tt.wantRange)
				}
				if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
					t.Errorf("Content-Type = %q, want application/problem+json (body %q)", ct, body)
				}
				var doc struct {
					Status int `json:"status"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil || doc.Status != tt.wantStatus {
					t.Errorf("body = %q, want a problem+json document with status %d", body, tt.wantStatus)
				}

				resolved, rendered, hooked, reported, failed := p.counts()
				if resolved != 1 {
					t.Errorf("error handler resolved %d times, want 1 (pipeline never entered)", resolved)
				}
				if rendered != 1 {
					t.Errorf("render rule ran %d times, want 1", rendered)
				}
				if len(hooked) != 1 || hooked[0] != tt.wantStatus {
					t.Errorf("BeforeRender hook saw statuses %v, want [%d]", hooked, tt.wantStatus)
				}
				if reported != tt.wantFailures {
					t.Errorf("reports = %d, want %d", reported, tt.wantFailures)
				}
				if failed != tt.wantFailures {
					t.Errorf("RequestFailed events = %d, want %d", failed, tt.wantFailures)
				}
			})
		}
	}
}
