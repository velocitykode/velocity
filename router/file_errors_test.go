package router

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// fileErrorRoot builds a root holding ok.txt, locked.txt (mode 0, so
// opening it fails with permission denied for a non-root user) and a sub
// directory, plus a symlink escaping it, and returns the opened *os.Root.
func fileErrorRoot(t *testing.T) *os.Root {
	t.Helper()
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "locked.txt"), []byte("locked"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "escape.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	return openTestRoot(t, dir)
}

// TestContext_FileHelpers_TypedErrors asserts File, Download and
// Attachment answer every failure with an HTTP error whose status is
// fixed by the case and whose cause stays reachable through errors.Is,
// writing nothing. An operational open failure (permission denied, a
// closed root) is the server's 500, never folded into ErrPathOutsideRoot.
func TestContext_FileHelpers_TypedErrors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		noRoot     bool
		closedRoot bool
		needsUser  bool // skipped as root, which opens a mode-0 file
		wantStatus int
		wantCause  error
		notCause   error
	}{
		{name: "no root", path: "ok.txt", noRoot: true, wantStatus: http.StatusInternalServerError, wantCause: ErrNilRoot},
		{name: "traversal", path: "../../etc/passwd", wantStatus: http.StatusBadRequest, wantCause: ErrInvalidFilePath},
		{name: "absolute", path: "/etc/passwd", wantStatus: http.StatusBadRequest, wantCause: ErrInvalidFilePath},
		{name: "nul byte", path: "ok\x00.txt", wantStatus: http.StatusBadRequest, wantCause: ErrInvalidFilePath},
		{name: "missing file", path: "nope.txt", wantStatus: http.StatusNotFound, wantCause: os.ErrNotExist},
		{name: "symlink escape", path: "escape.txt", wantStatus: http.StatusNotFound, wantCause: ErrPathOutsideRoot},
		{name: "directory", path: "sub", wantStatus: http.StatusNotFound, wantCause: ErrIsDirectory},
		{name: "not a directory", path: "ok.txt/inner", wantStatus: http.StatusNotFound, wantCause: ErrPathOutsideRoot},
		{name: "permission denied", path: "locked.txt", needsUser: true, wantStatus: http.StatusInternalServerError, wantCause: fs.ErrPermission, notCause: ErrPathOutsideRoot},
		{name: "closed root", path: "ok.txt", closedRoot: true, wantStatus: http.StatusInternalServerError, wantCause: fs.ErrClosed, notCause: ErrPathOutsideRoot},
	}
	helpers := map[string]func(*Context, string) error{
		"File":       func(c *Context, p string) error { return c.File(p) },
		"Download":   func(c *Context, p string) error { return c.Download(p, "f.txt") },
		"Attachment": func(c *Context, p string) error { return c.Attachment(p, "f.txt") },
	}
	root := fileErrorRoot(t)
	closed := openTestRoot(t, t.TempDir())
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		for name, call := range helpers {
			t.Run(tt.name+"/"+name, func(t *testing.T) {
				if tt.needsUser && os.Geteuid() == 0 {
					t.Skip("root opens a mode-0 file")
				}
				w := httptest.NewRecorder()
				c := NewContext(w, httptest.NewRequest(http.MethodGet, "/f", nil))
				switch {
				case tt.closedRoot:
					c.fileRoot = closed
				case !tt.noRoot:
					c.fileRoot = root
				}
				err := call(c, tt.path)
				var se contract.StatusError
				if !errors.As(err, &se) {
					t.Fatalf("err = %v (%T), want a StatusError", err, err)
				}
				if se.StatusCode() != tt.wantStatus {
					t.Errorf("status = %d, want %d", se.StatusCode(), tt.wantStatus)
				}
				if !errors.Is(err, tt.wantCause) {
					t.Errorf("err = %v, want errors.Is %v", err, tt.wantCause)
				}
				if tt.notCause != nil && errors.Is(err, tt.notCause) {
					t.Errorf("err = %v, must not match %v", err, tt.notCause)
				}
				if w.Body.Len() != 0 || w.Header().Get("Content-Disposition") != "" || w.Header().Get("Cache-Control") != "" {
					t.Errorf("wrote body %q headers %v, want nothing", w.Body.String(), w.Header())
				}
			})
		}
	}
}

// TestContext_FileHelpers_OriginIsTheCaller asserts the origin the file
// errors record is the handler that called File, Download or Attachment.
func TestContext_FileHelpers_OriginIsTheCaller(t *testing.T) {
	c := NewContext(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/f", nil))
	for name, err := range map[string]error{
		"File":       c.File("x"),
		"Download":   c.Download("x", "x"),
		"Attachment": c.Attachment("x", "x"),
	} {
		var he *contract.HTTPError
		if !errors.As(err, &he) {
			t.Fatalf("%s: err = %v, want *contract.HTTPError", name, err)
		}
		if got := he.Origin(); !strings.Contains(got, "file_errors_test.go") || !strings.Contains(got, "TestContext_FileHelpers_OriginIsTheCaller") {
			t.Errorf("%s: origin = %q, want this test", name, got)
		}
	}
}

// TestSaveFile_TypedCauses asserts SaveFile returns the file sentinels.
func TestSaveFile_TypedCauses(t *testing.T) {
	c := NewContext(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/f", nil))
	if err := c.SaveFile(nil, "x.txt"); !errors.Is(err, ErrNilRoot) {
		t.Errorf("no root: err = %v, want ErrNilRoot", err)
	}
	c.fileRoot = openTestRoot(t, t.TempDir())
	if err := c.SaveFile(nil, "../x.txt"); !errors.Is(err, ErrInvalidFilePath) {
		t.Errorf("traversal: err = %v, want ErrInvalidFilePath", err)
	}
}
