package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// fileErrorRoot builds a root holding ok.txt and a sub directory, plus a
// symlink escaping it, and returns the opened *os.Root.
func fileErrorRoot(t *testing.T) *os.Root {
	t.Helper()
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("ok"), 0o600); err != nil {
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
// writing nothing.
func TestContext_FileHelpers_TypedErrors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		noRoot     bool
		wantStatus int
		wantCause  error
	}{
		{name: "no root", path: "ok.txt", noRoot: true, wantStatus: http.StatusInternalServerError, wantCause: ErrNilRoot},
		{name: "traversal", path: "../../etc/passwd", wantStatus: http.StatusBadRequest, wantCause: ErrInvalidFilePath},
		{name: "absolute", path: "/etc/passwd", wantStatus: http.StatusBadRequest, wantCause: ErrInvalidFilePath},
		{name: "nul byte", path: "ok\x00.txt", wantStatus: http.StatusBadRequest, wantCause: ErrInvalidFilePath},
		{name: "missing file", path: "nope.txt", wantStatus: http.StatusNotFound, wantCause: os.ErrNotExist},
		{name: "symlink escape", path: "escape.txt", wantStatus: http.StatusNotFound, wantCause: ErrPathOutsideRoot},
		{name: "directory", path: "sub", wantStatus: http.StatusNotFound, wantCause: ErrIsDirectory},
	}
	helpers := map[string]func(*Context, string) error{
		"File":       func(c *Context, p string) error { return c.File(p) },
		"Download":   func(c *Context, p string) error { return c.Download(p, "f.txt") },
		"Attachment": func(c *Context, p string) error { return c.Attachment(p, "f.txt") },
	}
	root := fileErrorRoot(t)
	for _, tt := range tests {
		for name, call := range helpers {
			t.Run(tt.name+"/"+name, func(t *testing.T) {
				w := httptest.NewRecorder()
				c := NewContext(w, httptest.NewRequest(http.MethodGet, "/f", nil))
				if !tt.noRoot {
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
