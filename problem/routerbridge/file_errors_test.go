package routerbridge

import (
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

// TestInstall_FileHelperErrors asserts every ctx.File, ctx.Download and
// ctx.Attachment failure answers the same status through the standalone
// router and through the pipeline, never shows its cause to the client,
// and is reported (logged by the standalone router) only for the 500: a
// missing root, or an operational open failure such as permission denied.
func TestInstall_FileHelperErrors(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "escape.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "locked.txt"), []byte("locked"), 0o000); err != nil {
		t.Fatal(err)
	}
	causeText := []string{"os.Root", "invalid file path", "escapes root", "no such file", "is a directory", "permission denied", "velocity/router"}

	tests := []struct {
		name        string
		root        string
		path        string
		needsUser   bool // skipped as root, which opens a mode-0 file
		wantStatus  int
		wantReports int
	}{
		{name: "no root", root: filepath.Join(dir, "absent"), path: "x.txt", wantStatus: http.StatusInternalServerError, wantReports: 1},
		{name: "malformed path", root: dir, path: "../x.txt", wantStatus: http.StatusBadRequest},
		{name: "missing file", root: dir, path: "nope.txt", wantStatus: http.StatusNotFound},
		{name: "symlink escape", root: dir, path: "escape.txt", wantStatus: http.StatusNotFound},
		{name: "directory", root: dir, path: "sub", wantStatus: http.StatusNotFound},
		{name: "permission denied", root: dir, path: "locked.txt", needsUser: true, wantStatus: http.StatusInternalServerError, wantReports: 1},
	}
	helpers := map[string]func(*router.Context, string) error{
		"File":       func(c *router.Context, p string) error { return c.File(p) },
		"Download":   func(c *router.Context, p string) error { return c.Download(p, "f.txt") },
		"Attachment": func(c *router.Context, p string) error { return c.Attachment(p, "f.txt") },
	}
	for _, tt := range tests {
		for helper, call := range helpers {
			for _, accept := range []string{"application/json", "text/html"} {
				t.Run(tt.name+"/"+helper+"/"+accept, func(t *testing.T) {
					if tt.needsUser && os.Geteuid() == 0 {
						t.Skip("root opens a mode-0 file")
					}
					handler := func(c *router.Context) error { return call(c, tt.path) }

					var mu sync.Mutex
					logged := 0
					standalone := router.New()
					standalone.SetFileRoot(tt.root)
					standalone.SetErrorLogger(func(string, ...any) { mu.Lock(); logged++; mu.Unlock() })
					standalone.Get("/f", handler)

					rec := &recordingReporter{}
					h := problem.NewHandler(problem.WithReporters(rec))
					h.SetDebug(false)
					installed := router.New()
					installed.SetFileRoot(tt.root)
					Install(installed, WithHandler(func() contract.ErrorHandler { return h }))
					installed.Get("/f", handler)

					for name, r := range map[string]*router.VelocityRouterV2{"standalone": standalone, "pipeline": installed} {
						w := httptest.NewRecorder()
						req := httptest.NewRequest(http.MethodGet, "/f", nil)
						req.Header.Set("Accept", accept)
						r.ServeHTTP(w, req)
						if w.Code != tt.wantStatus {
							t.Errorf("%s: status = %d, want %d", name, w.Code, tt.wantStatus)
						}
						for _, s := range causeText {
							if strings.Contains(w.Body.String(), s) {
								t.Errorf("%s: body leaks %q: %s", name, s, w.Body.String())
							}
						}
						if w.Header().Get("Content-Disposition") != "" {
							t.Errorf("%s: Content-Disposition set on an error", name)
						}
					}
					mu.Lock()
					defer mu.Unlock()
					if logged != tt.wantReports {
						t.Errorf("standalone error logs = %d, want %d", logged, tt.wantReports)
					}
					if rec.count() != tt.wantReports {
						t.Errorf("pipeline reports = %d, want %d", rec.count(), tt.wantReports)
					}
				})
			}
		}
	}
}
