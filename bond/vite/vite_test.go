package vite

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeManifest creates a minimal Vite manifest at the path the
// @velocitykode/velocity-vite-plugin emits — public/build/manifest.json
// directly, no `.vite/` subdir.
func writeManifest(t *testing.T, dir string, body string) {
	t.Helper()
	mdir := filepath.Join(dir, "public", "build")
	if err := os.MkdirAll(mdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mdir, "manifest.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newHelperIn(t *testing.T, dir string) *Helper {
	t.Helper()
	return New(WithPublicPath(filepath.Join(dir, "public")))
}

func TestTags_HotMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteHotFile(filepath.Join(dir, "public"), "", "http://localhost:5173"); err != nil {
		t.Fatal(err)
	}
	h := newHelperIn(t, dir)

	got, err := h.Tags("resources/js/app.tsx")
	if err != nil {
		t.Fatalf("Tags returned error: %v", err)
	}
	s := string(got)
	for _, want := range []string{
		`http://localhost:5173/@vite/client`,
		`http://localhost:5173/resources/js/app.tsx`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("hot output missing %q\nfull: %s", want, s)
		}
	}
	// In hot mode the manifest is irrelevant — none of the prod
	// scaffolding should leak through.
	for _, unwanted := range []string{`/build/`, `modulepreload`} {
		if strings.Contains(s, unwanted) {
			t.Errorf("hot output should not contain %q\nfull: %s", unwanted, s)
		}
	}
}

func TestTags_ProdMode_EmitsCSSAndScript(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{
	  "resources/js/app.tsx": {
	    "file": "assets/app-ABC.js",
	    "src": "resources/js/app.tsx",
	    "isEntry": true,
	    "css": ["assets/app-XYZ.css"]
	  }
	}`)
	h := newHelperIn(t, dir)

	got, err := h.Tags("resources/js/app.tsx")
	if err != nil {
		t.Fatalf("Tags returned error: %v", err)
	}
	s := string(got)

	wantStyle := `<link rel="stylesheet" href="/build/assets/app-XYZ.css">`
	wantScript := `<script type="module" src="/build/assets/app-ABC.js"></script>`
	if !strings.Contains(s, wantStyle) {
		t.Errorf("prod output missing stylesheet tag\nwant substring: %s\nfull: %s", wantStyle, s)
	}
	if !strings.Contains(s, wantScript) {
		t.Errorf("prod output missing script tag\nwant substring: %s\nfull: %s", wantScript, s)
	}
	// The stylesheet must come before the script — browsers should
	// have CSS in flight before the JS executes.
	if strings.Index(s, wantStyle) > strings.Index(s, wantScript) {
		t.Errorf("stylesheet must precede script\nfull: %s", s)
	}
}

func TestTags_ProdMode_PreloadsImports(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{
	  "_chunk-A.js": {"file": "assets/chunk-A-1.js"},
	  "_chunk-B.js": {"file": "assets/chunk-B-2.js", "css": ["assets/chunk-B.css"]},
	  "resources/js/app.tsx": {
	    "file": "assets/app-ABC.js",
	    "src": "resources/js/app.tsx",
	    "isEntry": true,
	    "imports": ["_chunk-A.js", "_chunk-B.js"]
	  }
	}`)
	h := newHelperIn(t, dir)

	got, err := h.Tags("resources/js/app.tsx")
	if err != nil {
		t.Fatalf("Tags returned error: %v", err)
	}
	s := string(got)

	for _, want := range []string{
		`<link rel="modulepreload" href="/build/assets/chunk-A-1.js">`,
		`<link rel="modulepreload" href="/build/assets/chunk-B-2.js">`,
		`<link rel="stylesheet" href="/build/assets/chunk-B.css">`,
		`<script type="module" src="/build/assets/app-ABC.js"></script>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing tag %q\nfull: %s", want, s)
		}
	}
}

func TestTags_ProdMode_MissingManifestReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := newHelperIn(t, dir)

	got, err := h.Tags("resources/js/app.tsx")
	if err == nil {
		t.Fatal("expected error for missing manifest")
	}
	if !errors.Is(err, ErrManifestNotFound) {
		t.Errorf("error chain missing ErrManifestNotFound: %v", err)
	}
	if !strings.Contains(string(got), "<!-- vite:") {
		t.Errorf("expected error comment in output, got: %s", got)
	}
}

func TestTags_ProdMode_UnknownEntrypointReturnsError(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{
	  "resources/js/app.tsx": {"file": "assets/app.js", "isEntry": true}
	}`)
	h := newHelperIn(t, dir)

	_, err := h.Tags("resources/js/missing.tsx")
	if !errors.Is(err, ErrEntrypointNotInManifest) {
		t.Fatalf("want ErrEntrypointNotInManifest, got %v", err)
	}
}

func TestManifest_RefreshOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, `{
	  "resources/js/app.tsx": {"file": "assets/v1.js", "isEntry": true}
	}`)
	h := newHelperIn(t, dir)

	got, err := h.Tags("resources/js/app.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "/build/assets/v1.js") {
		t.Fatalf("first read missing v1.js: %s", got)
	}

	// Bump mtime forward so the cache invalidates regardless of how
	// fast this test runs.
	mpath := filepath.Join(dir, "public", "build", "manifest.json")
	future := mustFutureMtime(t, mpath)
	if err := os.WriteFile(mpath, []byte(`{
	  "resources/js/app.tsx": {"file": "assets/v2.js", "isEntry": true}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(mpath, future, future); err != nil {
		t.Fatal(err)
	}

	got, err = h.Tags("resources/js/app.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "/build/assets/v2.js") {
		t.Fatalf("second read missing v2.js: %s", got)
	}
}

func TestAsset(t *testing.T) {
	dir := t.TempDir()

	// Hot mode: dev URL passthrough.
	if err := os.MkdirAll(filepath.Join(dir, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteHotFile(filepath.Join(dir, "public"), "", "http://localhost:5173"); err != nil {
		t.Fatal(err)
	}
	h := newHelperIn(t, dir)
	got, err := h.Asset("resources/js/app.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://localhost:5173/resources/js/app.tsx" {
		t.Errorf("hot Asset = %q", got)
	}

	// Prod mode: manifest lookup.
	if err := RemoveHotFile(filepath.Join(dir, "public"), ""); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, `{
	  "resources/js/app.tsx": {"file": "assets/app-ABC.js", "isEntry": true}
	}`)
	got, err = h.Asset("resources/js/app.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/build/assets/app-ABC.js" {
		t.Errorf("prod Asset = %q", got)
	}
}

func TestHotURL_RejectsMalformedFile(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"non-http scheme", "javascript:alert(1)"},
		{"no scheme", "localhost:5173"},
		{"embedded space", "http://localhost:5173 evil"},
		{"embedded angle", "http://x<script>"},
		{"control char", "http://x\x01y"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			pub := filepath.Join(dir, "public")
			if err := os.MkdirAll(pub, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pub, "hot"), []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			h := newHelperIn(t, dir)
			_, err := h.Tags("resources/js/app.tsx")
			if err == nil {
				t.Fatalf("expected error for malformed hot file content %q", tc.content)
			}
		})
	}
}

func TestErrComment_NoPathLeakAndNoCommentEscape(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := newHelperIn(t, dir)

	got, _ := h.Tags("resources/js/app.tsx")
	s := string(got)
	// No filesystem path leak.
	if strings.Contains(s, dir) {
		t.Errorf("error comment leaked filesystem path: %s", s)
	}
	// No comment-escape sequence.
	if strings.Contains(s, "--&gt;") || strings.Count(s, "-->") != 1 {
		// Exactly one closing "-->" — the comment terminator we
		// emit. Any second one would mean injection.
		t.Errorf("comment integrity broken: %s", s)
	}
	if !strings.Contains(s, "manifest not found") {
		t.Errorf("expected short message, got: %s", s)
	}
}

func TestReactRefreshTag_HotMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteHotFile(filepath.Join(dir, "public"), "", "http://localhost:5173"); err != nil {
		t.Fatal(err)
	}
	h := newHelperIn(t, dir)

	got, err := h.ReactRefreshTag()
	if err != nil {
		t.Fatalf("ReactRefreshTag: %v", err)
	}
	s := string(got)
	for _, want := range []string{
		`http://localhost:5173/@react-refresh`,
		`__vite_plugin_react_preamble_installed__`,
		`RefreshRuntime.injectIntoGlobalHook`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("preamble missing %q\nfull: %s", want, s)
		}
	}
}

func TestReactRefreshTag_ProdModeReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := newHelperIn(t, dir)

	got, err := h.ReactRefreshTag()
	if err != nil {
		t.Fatalf("ReactRefreshTag: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty in prod mode, got: %s", got)
	}
}

func TestWriteAndRemoveHotFile(t *testing.T) {
	dir := t.TempDir()
	pub := filepath.Join(dir, "public")
	if err := WriteHotFile(pub, "", "http://localhost:5173"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(pub, "hot"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "http://localhost:5173" {
		t.Errorf("hot file contents = %q", b)
	}
	if err := RemoveHotFile(pub, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pub, "hot")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected hot file to be removed")
	}
	// Removing an absent hot file must not error — `vel serve` may
	// retry shutdown on signal storms.
	if err := RemoveHotFile(pub, ""); err != nil {
		t.Errorf("RemoveHotFile on missing file returned %v", err)
	}
}

// mustFutureMtime returns a timestamp guaranteed to be after the
// current mtime of path, so cache invalidation by mtime is observable
// even on filesystems with coarse (1s) timestamp resolution.
func mustFutureMtime(t *testing.T, path string) time.Time {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.ModTime().Add(2 * time.Second)
}
