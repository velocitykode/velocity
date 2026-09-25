//go:build darwin || linux

package routerbridge

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// TestInstall_FileServeFailureCauseStaysServerSide asserts a file the
// content server cannot size (a named pipe: its Seek fails) answers 500
// through the standalone router and the pipeline, is reported (logged by
// the standalone router) with the content server's own text as the cause,
// and never shows that text to the client outside debug mode.
func TestInstall_FileServeFailureCauseStaysServerSide(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe.txt")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	// Hold both ends open so opening the pipe for reading never blocks.
	holder, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Skipf("opening the pipe read-write unsupported: %v", err)
	}
	defer holder.Close()
	const causeText = "seeker can't seek"

	for _, accept := range []string{"application/json", "text/html"} {
		t.Run(accept, func(t *testing.T) {
			handler := func(c *router.Context) error { return c.File("pipe.txt") }

			var mu sync.Mutex
			var logged []string
			standalone := router.New()
			standalone.SetFileRoot(dir)
			standalone.SetErrorLogger(func(_ string, kvs ...any) {
				mu.Lock()
				defer mu.Unlock()
				for i := 0; i+1 < len(kvs); i += 2 {
					if kvs[i] == "error" {
						logged = append(logged, kvs[i+1].(string))
					}
				}
			})
			standalone.Get("/f", handler)

			var reported []string
			h := problem.NewHandler(problem.WithReporters(problem.NewCallbackReporter(func(err error, _ *problem.ErrorContext) {
				mu.Lock()
				reported = append(reported, err.Error())
				mu.Unlock()
			})))
			h.SetDebug(false)
			installed := router.New()
			installed.SetFileRoot(dir)
			Install(installed, WithHandler(func() contract.ErrorHandler { return h }))
			installed.Get("/f", handler)

			for name, r := range map[string]*router.VelocityRouterV2{"standalone": standalone, "pipeline": installed} {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/f", nil)
				req.Header.Set("Accept", accept)
				r.ServeHTTP(w, req)
				if w.Code != http.StatusInternalServerError {
					t.Errorf("%s: status = %d, want 500", name, w.Code)
				}
				if strings.Contains(w.Body.String(), "seek") {
					t.Errorf("%s: body leaks the cause: %s", name, w.Body.String())
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(logged) != 1 || !strings.Contains(logged[0], causeText) {
				t.Errorf("standalone error lines = %q, want one naming %q", logged, causeText)
			}
			if len(reported) != 1 || !strings.Contains(reported[0], causeText) {
				t.Errorf("pipeline reports = %q, want one naming %q", reported, causeText)
			}
		})
	}
}
