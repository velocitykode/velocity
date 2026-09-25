package router

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestSaveFile_OpenFailureIsNotContainment asserts an operational failure
// opening the destination (a mode-0 file: permission denied) comes back
// as the fs error, not folded into ErrPathOutsideRoot, while a
// destination under a path component that is not a directory still
// matches ErrPathOutsideRoot.
func TestSaveFile_OpenFailureIsNotContainment(t *testing.T) {
	tests := []struct {
		name      string
		dst       string
		needsUser bool // skipped as root, which opens a mode-0 file
		wantCause error
		notCause  error
	}{
		{name: "permission denied", dst: "locked.txt", needsUser: true, wantCause: fs.ErrPermission, notCause: ErrPathOutsideRoot},
		{name: "not a directory", dst: "plain.txt/inner.txt", wantCause: ErrPathOutsideRoot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.needsUser && os.Geteuid() == 0 {
				t.Skip("root opens a mode-0 file")
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "locked.txt"), []byte("locked"), 0o000); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("plain"), 0o600); err != nil {
				t.Fatal(err)
			}
			c, fh := newUploadContext(t, "upload.txt", []byte("save me"))
			c.fileRoot = openTestRoot(t, dir)
			err := c.SaveFile(fh, tt.dst)
			if !errors.Is(err, tt.wantCause) {
				t.Errorf("err = %v, want errors.Is %v", err, tt.wantCause)
			}
			if tt.notCause != nil && errors.Is(err, tt.notCause) {
				t.Errorf("err = %v, must not match %v", err, tt.notCause)
			}
		})
	}
}

// writeSpy is a ResponseRecorder that records whether anything was written
// through it.
type writeSpy struct {
	*httptest.ResponseRecorder
	wrote bool
}

func (w *writeSpy) WriteHeader(code int) { w.wrote = true; w.ResponseRecorder.WriteHeader(code) }

func (w *writeSpy) Write(p []byte) (int, error) { w.wrote = true; return w.ResponseRecorder.Write(p) }

// TestContext_FileHelpers_ServeContentErrors asserts a request the file
// cannot answer as asked (a Range past its end, a failed If-Match) comes
// back from File, Download and Attachment as a typed HTTP error at the
// status content serving chose, the 416 carrying Content-Range, with
// nothing written: no status, body, Cache-Control or Content-Disposition.
func TestContext_FileHelpers_ServeContentErrors(t *testing.T) {
	tests := []struct {
		name             string
		header           map[string]string
		wantStatus       int
		wantContentRange string
	}{
		{name: "unsatisfiable range", header: map[string]string{"Range": "bytes=100-200"}, wantStatus: http.StatusRequestedRangeNotSatisfiable, wantContentRange: "bytes */2"},
		{name: "failed precondition", header: map[string]string{"If-Match": `"nope"`}, wantStatus: http.StatusPreconditionFailed},
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
				w := &writeSpy{ResponseRecorder: httptest.NewRecorder()}
				req := httptest.NewRequest(http.MethodGet, "/f", nil)
				for k, v := range tt.header {
					req.Header.Set(k, v)
				}
				c := NewContext(w, req)
				c.fileRoot = root
				err := call(c, "ok.txt")
				var he *contract.HTTPError
				if !errors.As(err, &he) {
					t.Fatalf("err = %v (%T), want a *contract.HTTPError", err, err)
				}
				if he.StatusCode() != tt.wantStatus {
					t.Errorf("status = %d, want %d", he.StatusCode(), tt.wantStatus)
				}
				if got := he.Headers().Get("Content-Range"); got != tt.wantContentRange {
					t.Errorf("Content-Range = %q, want %q", got, tt.wantContentRange)
				}
				if got := he.Origin(); !strings.Contains(got, "file_errors_test.go") {
					t.Errorf("origin = %q, want this test file", got)
				}
				if w.wrote || len(w.Header()) != 0 {
					t.Errorf("wrote %v, headers %v; want nothing", w.wrote, w.Header())
				}
			})
		}
	}
}

// TestContext_FileHelpers_FailureDropsFileHeaders asserts a failed file
// answer leaves the response header without the file's representation
// headers the handler set before calling File: Content-Encoding, ETag,
// Last-Modified and Cache-Control are gone, so the body the error boundary
// writes next is not labelled with them, and an unrelated header stays.
func TestContext_FileHelpers_FailureDropsFileHeaders(t *testing.T) {
	tests := []struct {
		name       string
		header     map[string]string
		wantStatus int
	}{
		{name: "unsatisfiable range", header: map[string]string{"Range": "bytes=100-200"}, wantStatus: http.StatusRequestedRangeNotSatisfiable},
		{name: "failed precondition", header: map[string]string{"If-Match": `"nope"`}, wantStatus: http.StatusPreconditionFailed},
	}
	root := fileErrorRoot(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &writeSpy{ResponseRecorder: httptest.NewRecorder()}
			req := httptest.NewRequest(http.MethodGet, "/f", nil)
			for k, v := range tt.header {
				req.Header.Set(k, v)
			}
			c := NewContext(w, req)
			c.fileRoot = root
			h := c.Response.Header()
			h.Set("ETag", `"v1"`)
			h.Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
			h.Set("Content-Encoding", "gzip")
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
			h.Set("Vary", "Accept-Encoding")
			err := c.File("ok.txt")
			var he *contract.HTTPError
			if !errors.As(err, &he) || he.StatusCode() != tt.wantStatus {
				t.Fatalf("err = %v, want a %d HTTP error", err, tt.wantStatus)
			}
			for _, k := range []string{"ETag", "Last-Modified", "Content-Encoding", "Cache-Control"} {
				if got := w.Header().Get(k); got != "" {
					t.Errorf("%s = %q after the failed File, want it dropped", k, got)
				}
			}
			if got := w.Header().Get("Vary"); got != "Accept-Encoding" {
				t.Errorf("Vary = %q, want the handler's Accept-Encoding kept", got)
			}
			if w.wrote {
				t.Error("the failed File wrote to the response")
			}
		})
	}
}

// TestContext_FileHelpers_ServeContentPassThrough asserts a full response,
// a partial one and a 304 still pass through File and Download unchanged:
// status, body, Content-Range, Cache-Control and Content-Disposition.
func TestContext_FileHelpers_ServeContentPassThrough(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	tests := []struct {
		name             string
		header           map[string]string
		wantStatus       int
		wantBody         string
		wantContentRange string
	}{
		{name: "full", wantStatus: http.StatusOK, wantBody: "ok"},
		{name: "partial", header: map[string]string{"Range": "bytes=0-0"}, wantStatus: http.StatusPartialContent, wantBody: "o", wantContentRange: "bytes 0-0/2"},
		{name: "not modified", header: map[string]string{"If-Modified-Since": future}, wantStatus: http.StatusNotModified},
	}
	root := fileErrorRoot(t)
	for _, tt := range tests {
		for _, attach := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/attach=%v", tt.name, attach), func(t *testing.T) {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/f", nil)
				for k, v := range tt.header {
					req.Header.Set(k, v)
				}
				c := NewContext(w, req)
				c.fileRoot = root
				var err error
				if attach {
					err = c.Download("ok.txt", "f.txt")
				} else {
					err = c.File("ok.txt")
				}
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				if w.Code != tt.wantStatus || w.Body.String() != tt.wantBody {
					t.Errorf("response = %d %q, want %d %q", w.Code, w.Body.String(), tt.wantStatus, tt.wantBody)
				}
				if got := w.Header().Get("Content-Range"); got != tt.wantContentRange {
					t.Errorf("Content-Range = %q, want %q", got, tt.wantContentRange)
				}
				if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
					t.Errorf("Cache-Control = %q, want private, no-store", got)
				}
				if got := w.Header().Get("Content-Disposition"); (got != "") != attach {
					t.Errorf("Content-Disposition = %q, want set only for a download", got)
				}
			})
		}
	}
}

// TestServeContentWriter_Cause asserts the cause a serveContentWriter
// builds from a discarded answer: the first line of a 5xx body, without CR
// or LF, bounded by serveContentCauseLimit; nothing for a 4xx or an empty
// body.
func TestServeContentWriter_Cause(t *testing.T) {
	long := strings.Repeat("x", serveContentCauseLimit+50)
	tests := []struct {
		name   string
		status int
		body   []string
		want   string // "" for no cause
	}{
		{name: "5xx first line", status: http.StatusInternalServerError, body: []string{"seeker can't seek\n"}, want: "seeker can't seek"},
		{name: "5xx CRLF split", status: http.StatusInternalServerError, body: []string{"first\r", "\nsecond\n"}, want: "first"},
		{name: "5xx bounded", status: http.StatusBadGateway, body: []string{long, long}, want: long[:serveContentCauseLimit]},
		{name: "5xx empty body", status: http.StatusInternalServerError},
		{name: "4xx keeps no cause", status: http.StatusRequestedRangeNotSatisfiable, body: []string{"invalid range: failed to overlap\n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &writeSpy{ResponseRecorder: httptest.NewRecorder()}
			sw := newServeContentWriter(w)
			sw.WriteHeader(tt.status)
			for _, b := range tt.body {
				if n, err := sw.Write([]byte(b)); n != len(b) || err != nil {
					t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(b))
				}
			}
			if w.wrote {
				t.Error("the discarded answer reached the writer")
			}
			got := sw.cause()
			switch {
			case tt.want == "" && got != nil:
				t.Errorf("cause = %q, want none", got)
			case tt.want != "" && (got == nil || got.Error() != tt.want):
				t.Errorf("cause = %v, want %q", got, tt.want)
			}
		})
	}
}
