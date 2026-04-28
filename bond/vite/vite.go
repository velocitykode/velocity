// Package vite emits Vite asset tags from a Go template.
//
// It mirrors Laravel's Illuminate\Foundation\Vite helper: a single Tags
// call decides between the dev server (when public/hot exists) and the
// production manifest (public/build/.vite/manifest.json), and returns
// the appropriate <link>/<script> markup.
//
// Typical wiring through bond.Config.Funcs:
//
//	helper := vite.New()
//	cfg.Funcs = template.FuncMap{
//	    "vite": helper.Tags,
//	}
//
// Then in the root template:
//
//	{{ vite "resources/js/app.tsx" }}
//
// Dev mode is signalled by the presence of a "hot" file written by
// `vel serve` (or any equivalent process) containing the dev-server
// origin (e.g. "http://localhost:5173"). When the file is absent the
// helper reads the Vite manifest, looks up each entrypoint, and emits
// stylesheet links plus the entry script tag, with modulepreload links
// for each statically imported chunk.
package vite

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// Default file/directory names matching the @velocitykode/velocity-vite-plugin
// emission layout: manifest at <publicPath>/<buildDirectory>/manifest.json
// (no `.vite/` subdir — the plugin sets `build.manifest: 'manifest.json'`
// rather than letting Vite use its `.vite/manifest.json` default).
const (
	DefaultPublicPath       = "public"
	DefaultBuildDirectory   = "build"
	DefaultHotFile          = "hot"
	DefaultManifestFilename = "manifest.json"
	DefaultManifestSubdir   = ""
)

// ErrManifestNotFound is returned when the Vite manifest is missing in
// production mode. The caller is almost always the root template, so the
// message includes the resolved path to make misconfigured deployments
// (typical cause: forgot to run `bun run build`) easy to diagnose.
var ErrManifestNotFound = errors.New("vite: manifest not found")

// ErrEntrypointNotInManifest indicates the requested entrypoint key is
// missing from the manifest. The build either did not include the file
// as an entry, or the template requested a path with a typo.
var ErrEntrypointNotInManifest = errors.New("vite: entrypoint not in manifest")

// Helper resolves Vite assets to HTML tags. It is safe for concurrent
// use; the manifest is cached in memory and re-read when the file's
// modification time changes (zero overhead in production where the
// manifest is immutable, automatic refresh after `bun run build`).
type Helper struct {
	publicPath       string
	buildDirectory   string
	hotFile          string
	manifestFilename string
	manifestSubdir   string

	mu         sync.RWMutex
	cached     manifest
	cachedPath string
	cachedMod  int64
}

// Option configures a Helper. Use the With* constructors.
type Option func(*Helper)

// WithPublicPath sets the directory containing built assets and the hot
// file (default "public").
func WithPublicPath(p string) Option { return func(h *Helper) { h.publicPath = p } }

// WithBuildDirectory sets the subdirectory of public/ where Vite writes
// its build output (default "build"). Must match `base` in vite.config.
func WithBuildDirectory(p string) Option { return func(h *Helper) { h.buildDirectory = p } }

// WithHotFile sets the hot-file name relative to public/ (default
// "hot"). The file's content is the dev-server origin URL.
func WithHotFile(name string) Option { return func(h *Helper) { h.hotFile = name } }

// WithManifestFilename sets the manifest filename inside the manifest
// subdirectory (default "manifest.json").
func WithManifestFilename(name string) Option {
	return func(h *Helper) { h.manifestFilename = name }
}

// WithManifestSubdir sets the subdirectory of the build directory where
// Vite writes the manifest (default ".vite", which matches Vite 5+).
// Pass "" to read the manifest directly from the build directory.
func WithManifestSubdir(p string) Option { return func(h *Helper) { h.manifestSubdir = p } }

// New constructs a Helper with sensible defaults overridden by opts.
func New(opts ...Option) *Helper {
	h := &Helper{
		publicPath:       DefaultPublicPath,
		buildDirectory:   DefaultBuildDirectory,
		hotFile:          DefaultHotFile,
		manifestFilename: DefaultManifestFilename,
		manifestSubdir:   DefaultManifestSubdir,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// IsRunningHot reports whether the dev server is active. The signal is
// the presence of the hot file — written by `vel serve` when starting
// Vite, removed on shutdown.
func (h *Helper) IsRunningHot() bool {
	_, err := os.Stat(h.hotFilePath())
	return err == nil
}

// Tags returns the HTML to inject for the given entrypoints. In dev
// mode it emits the @vite/client script plus one script per entrypoint
// pointed at the dev server. In production mode it walks the manifest
// to emit stylesheet links, modulepreload links for static imports,
// and the entry script.
//
// Returned errors render as an HTML comment containing the message —
// templates cannot signal errors directly, and a comment is far less
// catastrophic in a deployed page than a panic or a 500. The error is
// also returned so callers that want strict behavior can wrap Tags.
func (h *Helper) Tags(entrypoints ...string) (template.HTML, error) {
	if h.IsRunningHot() {
		hot, err := h.hotURL()
		if err != nil {
			return errComment(err), err
		}
		var b strings.Builder
		b.WriteString(scriptTag(joinURL(hot, "@vite/client")))
		for _, ep := range entrypoints {
			b.WriteString(scriptTag(joinURL(hot, ep)))
		}
		return template.HTML(b.String()), nil
	}

	m, err := h.manifest()
	if err != nil {
		return errComment(err), err
	}

	var (
		preloads     []string
		stylesheets  []string
		scripts      []string
		seenPreloads = map[string]struct{}{}
		seenCSS      = map[string]struct{}{}
	)

	addPreload := func(file string) {
		if _, ok := seenPreloads[file]; ok {
			return
		}
		seenPreloads[file] = struct{}{}
		preloads = append(preloads, modulePreloadTag(h.assetURL(file)))
	}
	addCSS := func(file string) {
		if _, ok := seenCSS[file]; ok {
			return
		}
		seenCSS[file] = struct{}{}
		stylesheets = append(stylesheets, stylesheetTag(h.assetURL(file)))
	}

	for _, ep := range entrypoints {
		chunk, ok := m[ep]
		if !ok {
			err := fmt.Errorf("%w: %s", ErrEntrypointNotInManifest, ep)
			return errComment(err), err
		}

		// Preload chunks the entry imports — gives the browser a head
		// start while it parses the entry script. Matches Laravel's
		// behavior in Vite::__invoke.
		for _, imp := range chunk.Imports {
			if dep, ok := m[imp]; ok {
				addPreload(dep.File)
				for _, css := range dep.CSS {
					addCSS(css)
				}
			}
		}
		for _, css := range chunk.CSS {
			addCSS(css)
		}
		scripts = append(scripts, scriptTag(h.assetURL(chunk.File)))
	}

	out := strings.Join(preloads, "") + strings.Join(stylesheets, "") + strings.Join(scripts, "")
	return template.HTML(out), nil
}

// ReactRefreshTag returns the React Fast Refresh preamble script when
// running against the Vite dev server, or an empty string in production.
// The preamble must execute before @vite/client loads, so the typical
// template usage is:
//
//	{{ viteReactRefresh }}
//	{{ vite "resources/js/app.tsx" }}
//
// Apps that don't use @vitejs/plugin-react can skip this helper —
// manifest mode never emits the preamble, so the prod path is unchanged.
func (h *Helper) ReactRefreshTag() (template.HTML, error) {
	if !h.IsRunningHot() {
		return "", nil
	}
	hot, err := h.hotURL()
	if err != nil {
		return errComment(err), err
	}
	// The script body is what @vitejs/plugin-react injects into the
	// entry HTML in dev mode. We render it inline here because the
	// app's HTML is rendered by the Go template engine, not by Vite's
	// transformIndexHtml — so the auto-injection never happens.
	const preamble = `<script type="module">
import RefreshRuntime from "%s/@react-refresh"
RefreshRuntime.injectIntoGlobalHook(window)
window.$RefreshReg$ = () => {}
window.$RefreshSig$ = () => (type) => type
window.__vite_plugin_react_preamble_installed__ = true
</script>`
	return template.HTML(fmt.Sprintf(preamble, template.HTMLEscapeString(hot))), nil
}

// Asset returns the public URL for an asset path. In dev mode it is
// served by the Vite dev server; in production it is rooted at the
// build directory under the public path.
func (h *Helper) Asset(file string) (string, error) {
	if h.IsRunningHot() {
		hot, err := h.hotURL()
		if err != nil {
			return "", err
		}
		return joinURL(hot, file), nil
	}
	m, err := h.manifest()
	if err != nil {
		return "", err
	}
	chunk, ok := m[file]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrEntrypointNotInManifest, file)
	}
	return h.assetURL(chunk.File), nil
}

// hotFilePath returns the absolute filesystem path to the hot file.
func (h *Helper) hotFilePath() string {
	return filepath.Join(h.publicPath, h.hotFile)
}

// hotURL reads the dev-server origin from the hot file. The file's
// trailing whitespace is trimmed — Vite writes a newline. The URL is
// validated to start with http:// or https:// so a corrupt hot file
// cannot inject arbitrary content into the script tags Tags() emits.
func (h *Helper) hotURL() (string, error) {
	b, err := os.ReadFile(h.hotFilePath())
	if err != nil {
		return "", fmt.Errorf("vite: read hot file: %w", err)
	}
	url := strings.TrimRight(string(b), " \r\n\t")
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", errInvalidHotURL
	}
	// Reject embedded whitespace or control bytes — a vite plugin
	// writes a single-line URL; anything else is suspicious.
	for _, r := range url {
		if r < 0x20 || r == ' ' || r == '"' || r == '<' || r == '>' {
			return "", errInvalidHotURL
		}
	}
	return url, nil
}

// errInvalidHotURL is returned when public/hot does not contain a
// well-formed http(s) URL. The most likely cause is a corrupt or
// hand-written hot file; no scenario short of filesystem tampering
// should produce one.
var errInvalidHotURL = errors.New("vite: hot file does not contain a valid http(s) URL")

// assetURL returns the absolute URL path for a built asset path
// relative to the build directory (e.g. "assets/app-XXX.js" →
// "/build/assets/app-XXX.js").
func (h *Helper) assetURL(file string) string {
	return "/" + path.Join(h.buildDirectory, file)
}

// manifest returns the parsed manifest, refreshing the cache if the
// file's mtime has changed. The cache is keyed by absolute path so a
// Helper reconfigured at runtime (e.g. in tests) does not return stale
// data from a different path.
func (h *Helper) manifest() (manifest, error) {
	manifestPath := h.manifestPath()

	st, err := os.Stat(manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrManifestNotFound, manifestPath)
		}
		return nil, fmt.Errorf("vite: stat manifest: %w", err)
	}

	mod := st.ModTime().UnixNano()

	h.mu.RLock()
	if h.cached != nil && h.cachedPath == manifestPath && h.cachedMod == mod {
		m := h.cached
		h.mu.RUnlock()
		return m, nil
	}
	h.mu.RUnlock()

	h.mu.Lock()
	defer h.mu.Unlock()
	// Re-check under write lock to avoid double-load when many
	// goroutines miss the cache simultaneously.
	if h.cached != nil && h.cachedPath == manifestPath && h.cachedMod == mod {
		return h.cached, nil
	}

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("vite: read manifest: %w", err)
	}
	parsed := manifest{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("vite: parse manifest: %w", err)
	}

	h.cached = parsed
	h.cachedPath = manifestPath
	h.cachedMod = mod
	return parsed, nil
}

// manifestPath returns the absolute path to the manifest file.
func (h *Helper) manifestPath() string {
	parts := []string{h.publicPath, h.buildDirectory}
	if h.manifestSubdir != "" {
		parts = append(parts, h.manifestSubdir)
	}
	parts = append(parts, h.manifestFilename)
	return filepath.Join(parts...)
}

// manifest is the parsed shape of Vite's manifest.json. We only model
// the fields Tags walks; unknown fields are ignored.
type manifest map[string]chunk

// chunk is one entry in the Vite manifest. Field names match the JSON
// keys Vite emits (file, src, css, imports, isEntry).
type chunk struct {
	File    string   `json:"file"`
	Src     string   `json:"src,omitempty"`
	CSS     []string `json:"css,omitempty"`
	Imports []string `json:"imports,omitempty"`
	IsEntry bool     `json:"isEntry,omitempty"`
}

func scriptTag(src string) string {
	return `<script type="module" src="` + template.HTMLEscapeString(src) + `"></script>`
}

func stylesheetTag(href string) string {
	return `<link rel="stylesheet" href="` + template.HTMLEscapeString(href) + `">`
}

func modulePreloadTag(href string) string {
	return `<link rel="modulepreload" href="` + template.HTMLEscapeString(href) + `">`
}

// errComment emits a minimal HTML comment for a helper error. The
// message is kept short and stripped of "-->" so a stray sequence in an
// error chain cannot prematurely close the comment. Full filesystem
// paths and stack traces are intentionally omitted — they belong in the
// server log, not in HTML returned to a browser.
func errComment(err error) template.HTML {
	msg := err.Error()
	// Trim chain noise: the leading sentinel name is enough for the
	// page comment; the wrapping fmt.Errorf adds the path/entrypoint
	// which we do not want to leak.
	switch {
	case errors.Is(err, ErrManifestNotFound):
		msg = "manifest not found"
	case errors.Is(err, ErrEntrypointNotInManifest):
		msg = "entrypoint not in manifest"
	}
	msg = strings.ReplaceAll(msg, "-->", "--&gt;")
	return template.HTML("<!-- vite: " + template.HTMLEscapeString(msg) + " -->")
}

// joinURL concatenates a base URL and a relative path with exactly one
// slash between them. Both inputs may carry trailing/leading slashes.
func joinURL(base, rel string) string {
	base = strings.TrimRight(base, "/")
	rel = strings.TrimLeft(rel, "/")
	return base + "/" + rel
}

// WriteHotFile writes the dev-server origin to the hot file so the
// helper switches into dev mode. Intended for `vel serve` to call when
// it starts the Vite dev process.
func WriteHotFile(publicPath, hotName, devURL string) error {
	if hotName == "" {
		hotName = DefaultHotFile
	}
	if publicPath == "" {
		publicPath = DefaultPublicPath
	}
	if err := os.MkdirAll(publicPath, 0o755); err != nil {
		return fmt.Errorf("vite: mkdir public: %w", err)
	}
	return os.WriteFile(filepath.Join(publicPath, hotName), []byte(devURL+"\n"), 0o644)
}

// RemoveHotFile removes the hot file, switching the helper back to
// manifest mode. Safe to call when the file is absent.
func RemoveHotFile(publicPath, hotName string) error {
	if hotName == "" {
		hotName = DefaultHotFile
	}
	if publicPath == "" {
		publicPath = DefaultPublicPath
	}
	err := os.Remove(filepath.Join(publicPath, hotName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
