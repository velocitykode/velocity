package router

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// staticModes runs a test against both static serving modes.
var staticModes = []struct {
	name  string
	setup func(r *VelocityRouterV2, dir string)
}{
	{"Static", func(r *VelocityRouterV2, dir string) { r.Static(dir) }},
	{"StaticFallback", func(r *VelocityRouterV2, dir string) { r.StaticFallback(dir) }},
}

// publicDir builds a public directory with a top-level file and an
// index-less build/ directory holding a file, and no top-level index.html.
func publicDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeStaticFile(t, dir, "app.css", "body{}")
	if err := os.Mkdir(filepath.Join(dir, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeStaticFile(t, filepath.Join(dir, "build"), "bundle.js", "bundle()")
	return dir
}

// assertNoListing fails when body looks like an http.FileServer
// directory listing of a directory holding any of names.
func assertNoListing(t *testing.T, body string, names ...string) {
	t.Helper()
	if strings.Contains(body, "<pre>") {
		t.Errorf("body is a directory listing: %q", body)
	}
	for _, name := range names {
		if strings.Contains(body, name) {
			t.Errorf("body names %q from the static directory: %q", name, body)
		}
	}
}

// TestStatic_RegisteredRootBeatsIndexlessDirectory asserts a registered
// "/" route answers "/" when the public directory has no index.html,
// instead of the file server listing the directory ahead of routing.
func TestStatic_RegisteredRootBeatsIndexlessDirectory(t *testing.T) {
	for _, mode := range staticModes {
		t.Run(mode.name, func(t *testing.T) {
			r := NewV2()
			var calls int32
			r.Use(chainCountMiddleware(&calls, "X-Test-MW"))
			mode.setup(r, publicDir(t))
			r.Get("/", func(c *Context) error {
				return c.String(http.StatusOK, "home")
			})

			w := serve(r, http.MethodGet, "/")
			if w.Code != http.StatusOK || w.Body.String() != "home" {
				t.Fatalf("GET / = %d %q, want 200 %q from the route", w.Code, w.Body.String(), "home")
			}
			assertNoListing(t, w.Body.String(), "app.css", "build")
			if n := atomic.LoadInt32(&calls); n != 1 {
				t.Errorf("middleware ran %d times, want exactly 1", n)
			}
		})
	}
}

// TestStatic_IndexlessDirectoryIsARoutingMiss asserts a directory without
// an index.html is never listed nor redirected to its slashed form: with
// no route it is a 404 answered by routing, and a registered route for
// the path answers it.
func TestStatic_IndexlessDirectoryIsARoutingMiss(t *testing.T) {
	for _, mode := range staticModes {
		for _, target := range []string{"/", "/build", "/build/"} {
			t.Run(mode.name+"/no route "+target, func(t *testing.T) {
				collector := newTestEventCollector()
				r := NewV2()
				r.SetEventDispatcher(collector.dispatch)
				mode.setup(r, publicDir(t))
				r.Get("/api", func(c *Context) error { return c.String(http.StatusOK, "api") })

				w := serve(r, http.MethodGet, target)
				if w.Code != http.StatusNotFound {
					t.Fatalf("GET %s = %d, want 404 (body %q)", target, w.Code, w.Body.String())
				}
				if loc := w.Header().Get("Location"); loc != "" {
					t.Errorf("GET %s redirected to %q, want no redirect", target, loc)
				}
				assertNoListing(t, w.Body.String(), "app.css", "bundle.js")
				for _, e := range collector.getEvents() {
					if routed, ok := e.(*RequestRouted); ok && routed.Route == "[static]" {
						t.Errorf("GET %s was dispatched to the static file server", target)
					}
				}
			})
		}
		for _, route := range []string{"/build", "/build/"} {
			for _, target := range []string{"/build", "/build/"} {
				t.Run(mode.name+"/route "+route+" "+target, func(t *testing.T) {
					r := NewV2()
					mode.setup(r, publicDir(t))
					r.Get(route, func(c *Context) error { return c.String(http.StatusOK, "build route") })

					w := serve(r, http.MethodGet, target)
					if w.Code != http.StatusOK || w.Body.String() != "build route" {
						t.Fatalf("GET %s = %d %q, want 200 from the route", target, w.Code, w.Body.String())
					}
				})
			}
		}
	}
}

// TestStatic_FilesAndIndexesStillServe asserts files, a file inside an
// index-less directory, and a directory's index.html are served as
// before, with the file server's redirect of a directory with an index to
// its slashed form kept.
func TestStatic_FilesAndIndexesStillServe(t *testing.T) {
	for _, mode := range staticModes {
		t.Run(mode.name, func(t *testing.T) {
			dir := publicDir(t)
			if err := os.Mkdir(filepath.Join(dir, "docs"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeStaticFile(t, filepath.Join(dir, "docs"), "index.html", "<p>docs index</p>")
			r := NewV2()
			mode.setup(r, dir)

			for _, tc := range []struct{ target, body string }{
				{"/app.css", "body{}"},
				{"/build/bundle.js", "bundle()"},
				{"/docs/", "<p>docs index</p>"},
			} {
				w := serve(r, http.MethodGet, tc.target)
				if w.Code != http.StatusOK || w.Body.String() != tc.body {
					t.Errorf("GET %s = %d %q, want 200 %q", tc.target, w.Code, w.Body.String(), tc.body)
				}
			}

			w := serve(r, http.MethodGet, "/docs")
			if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "docs/" {
				t.Errorf("GET /docs = %d Location %q, want 301 to %q", w.Code, w.Header().Get("Location"), "docs/")
			}
		})
	}
}

// TestStatic_TopIndexServesRoot asserts a public directory whose top holds
// an index.html serves it for "/" under Static (static first), while under
// StaticFallback a registered "/" route still wins.
func TestStatic_TopIndexServesRoot(t *testing.T) {
	for _, mode := range staticModes {
		t.Run(mode.name, func(t *testing.T) {
			dir := publicDir(t)
			writeStaticFile(t, dir, "index.html", "<p>spa</p>")
			r := NewV2()
			mode.setup(r, dir)
			r.Get("/", func(c *Context) error { return c.String(http.StatusOK, "home") })

			want := "<p>spa</p>"
			if r.staticFallbackOnly {
				want = "home"
			}
			if w := serve(r, http.MethodGet, "/"); w.Code != http.StatusOK || w.Body.String() != want {
				t.Errorf("GET / = %d %q, want 200 %q", w.Code, w.Body.String(), want)
			}
		})
	}
}

// TestStatic_IndexThatIsADirectoryIsNoIndex asserts an index.html that is
// itself a directory does not count as an index: the directory holding it
// is a miss and neither directory is listed.
func TestStatic_IndexThatIsADirectoryIsNoIndex(t *testing.T) {
	for _, mode := range staticModes {
		t.Run(mode.name, func(t *testing.T) {
			dir := publicDir(t)
			if err := os.Mkdir(filepath.Join(dir, "build", "index.html"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeStaticFile(t, filepath.Join(dir, "build", "index.html"), "inner.txt", "inner")
			r := NewV2()
			mode.setup(r, dir)

			for _, target := range []string{"/build/", "/build/index.html/"} {
				w := serve(r, http.MethodGet, target)
				if w.Code != http.StatusNotFound {
					t.Errorf("GET %s = %d, want 404 (body %q)", target, w.Code, w.Body.String())
				}
				assertNoListing(t, w.Body.String(), "bundle.js", "inner.txt")
			}
		})
	}
}

// TestStatic_UnreadableIndexIsForbidden asserts a directory whose
// index.html the server may not open answers 403, as any file it may not
// open does, instead of listing the directory.
func TestStatic_UnreadableIndexIsForbidden(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens files regardless of mode bits")
	}
	for _, mode := range staticModes {
		t.Run(mode.name, func(t *testing.T) {
			dir := publicDir(t)
			index := filepath.Join(dir, "build", "index.html")
			writeStaticFile(t, filepath.Join(dir, "build"), "index.html", "hidden")
			if err := os.Chmod(index, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(index, 0o600) })
			r := NewV2()
			mode.setup(r, dir)

			w := serve(r, http.MethodGet, "/build/")
			if w.Code != http.StatusForbidden {
				t.Fatalf("GET /build/ = %d, want 403 (body %q)", w.Code, w.Body.String())
			}
			assertNoListing(t, w.Body.String(), "bundle.js", "index.html")
		})
	}
}

// TestStatic_DotDotCannotEscapeTheRoot asserts a path climbing out of the
// public directory resolves inside it, so a file next to the directory is
// never served.
func TestStatic_DotDotCannotEscapeTheRoot(t *testing.T) {
	for _, mode := range staticModes {
		t.Run(mode.name, func(t *testing.T) {
			parent := t.TempDir()
			writeStaticFile(t, parent, "secret.txt", "secret-content")
			public := filepath.Join(parent, "public")
			if err := os.Mkdir(public, 0o700); err != nil {
				t.Fatal(err)
			}
			r := NewV2()
			mode.setup(r, public)

			for _, p := range []string{"/../secret.txt", "/../", "/.."} {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.URL.Path = p // bypass httptest cleaning
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if w.Code != http.StatusNotFound {
					t.Errorf("GET %s = %d, want 404 (body %q)", p, w.Code, w.Body.String())
				}
				assertNoListing(t, w.Body.String(), "secret.txt", "public")
				if strings.Contains(w.Body.String(), "secret-content") {
					t.Errorf("GET %s served a file outside the static root", p)
				}
			}
		})
	}
}

// countingFS counts the Opens and the Stats of the files it opens, and can
// make the index.html open fail after a number of successful ones.
type countingFS struct {
	root        http.FileSystem
	opens       atomic.Int32
	stats       atomic.Int32
	indexOpens  atomic.Int32
	indexBudget int32 // index.html opens that succeed; <0 means all
}

func (c *countingFS) Open(name string) (http.File, error) {
	c.opens.Add(1)
	if strings.HasSuffix(name, staticIndexPage) {
		if n := c.indexOpens.Add(1); c.indexBudget >= 0 && n > c.indexBudget {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
	}
	f, err := c.root.Open(name)
	if err != nil {
		return nil, err
	}
	return countingFile{File: f, stats: &c.stats}, nil
}

type countingFile struct {
	http.File
	stats *atomic.Int32
}

func (f countingFile) Stat() (fs.FileInfo, error) {
	f.stats.Add(1)
	return f.File.Stat()
}

// TestStatic_FileHitCost asserts a static file hit costs two Opens (the
// probe's and the file server's) and one Stat per Open, the file server's
// own Stat reusing the one read at open, and a miss one failed Open and no
// Stat.
func TestStatic_FileHitCost(t *testing.T) {
	dir := publicDir(t)
	counting := &countingFS{root: http.Dir(dir), indexBudget: -1}
	r := NewV2()
	r.Static(dir)
	r.useStaticRoot(counting)

	if w := serve(r, http.MethodGet, "/app.css"); w.Code != http.StatusOK {
		t.Fatalf("GET /app.css = %d, want 200", w.Code)
	}
	if opens, stats := counting.opens.Load(), counting.stats.Load(); opens != 2 || stats != 2 {
		t.Errorf("file hit: %d opens, %d stats, want 2 and 2", opens, stats)
	}

	counting.opens.Store(0)
	counting.stats.Store(0)
	if w := serve(r, http.MethodGet, "/missing.css"); w.Code != http.StatusNotFound {
		t.Fatalf("GET /missing.css = %d, want 404", w.Code)
	}
	if opens, stats := counting.opens.Load(), counting.stats.Load(); opens != 1 || stats != 0 {
		t.Errorf("miss: %d opens, %d stats, want 1 and 0", opens, stats)
	}
}

// TestStatic_IndexVanishingMidServeIsNotListed asserts that when the
// index.html of a directory disappears between the check that let the
// directory open and the file server's own open of the index, the file
// server answers an error rather than listing the directory.
func TestStatic_IndexVanishingMidServeIsNotListed(t *testing.T) {
	dir := publicDir(t)
	writeStaticFile(t, filepath.Join(dir, "build"), "index.html", "<p>build</p>")
	// The probe's index check and the file server's directory open each
	// open the index once; the file server's own index open then fails.
	counting := &countingFS{root: http.Dir(dir), indexBudget: 2}
	r := NewV2()
	var errorLines atomic.Int32
	r.SetErrorLogger(func(string, ...any) { errorLines.Add(1) })
	r.Static(dir)
	r.useStaticRoot(counting)

	w := serve(r, http.MethodGet, "/build/")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("GET /build/ = %d, want 500 (body %q)", w.Code, w.Body.String())
	}
	assertNoListing(t, w.Body.String(), "bundle.js", "index.html")
	if n := counting.indexOpens.Load(); n != 3 {
		t.Errorf("index.html opened %d times, want 3", n)
	}
	if n := errorLines.Load(); n != 1 {
		t.Errorf("error log lines = %d, want 1 (the 500 is reported)", n)
	}
}

// TestNoListingFS_DirectoryRefusesListing asserts a directory opened
// through noListingFS cannot be listed through either listing interface.
func TestNoListingFS_DirectoryRefusesListing(t *testing.T) {
	dir := publicDir(t)
	writeStaticFile(t, filepath.Join(dir, "build"), "index.html", "<p>build</p>")
	f, err := noListingFS{root: http.Dir(dir)}.Open("/build")
	if err != nil {
		t.Fatalf("Open(/build) = %v, want the directory", err)
	}
	defer f.Close()
	if _, ok := f.(fs.ReadDirFile); ok {
		t.Error("opened directory implements fs.ReadDirFile")
	}
	if entries, err := f.Readdir(-1); !errors.Is(err, errStaticListing) || len(entries) != 0 {
		t.Errorf("Readdir = %d entries, %v, want none and errStaticListing", len(entries), err)
	}
	if info, err := f.Stat(); err != nil || !info.IsDir() {
		t.Errorf("Stat = %v, %v, want the directory's FileInfo", info, err)
	}

	if _, err := (noListingFS{root: http.Dir(dir)}).Open("/"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open(/) without index.html = %v, want fs.ErrNotExist", err)
	}
}
