package router

import (
	"errors"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Task 2 — Redirect allowlist
// ---------------------------------------------------------------------------

func TestRedirect_RelativeAlwaysAllowed(t *testing.T) {
	c, rec := NewTestContext("GET", "/")
	if err := c.Redirect(http.StatusFound, "/dashboard"); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Location"); got != "/dashboard" {
		t.Errorf("Location = %q, want /dashboard", got)
	}
}

func TestRedirect_CrossHostDeniedByDefault(t *testing.T) {
	c, rec := NewTestContext("GET", "/")
	// Even if the Host header claims evil.com, an empty allowlist means
	// only relative paths are accepted.
	c.Request.Host = "evil.com"
	if err := c.Redirect(http.StatusFound, "https://evil.com/pwned"); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want / (cross-host must be rewritten)", got)
	}
}

func TestRedirect_ExplicitAllowlist(t *testing.T) {
	c, rec := NewTestContext("GET", "/")
	c.redirectAllowedHosts = []string{"trusted.example"}
	if err := c.Redirect(http.StatusFound, "https://trusted.example/home"); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Location"); got != "https://trusted.example/home" {
		t.Errorf("Location = %q, want passthrough for allow-listed host", got)
	}
}

func TestRedirect_ProtocolRelativeRejected(t *testing.T) {
	c, rec := NewTestContext("GET", "/")
	if err := c.Redirect(http.StatusFound, "//evil.com/pwned"); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want / (protocol-relative must be rewritten)", got)
	}
}

// ---------------------------------------------------------------------------
// Task 3 — RFC 5987/2231 Content-Disposition
// ---------------------------------------------------------------------------

func TestBuildContentDisposition_ASCII(t *testing.T) {
	got := buildContentDisposition("hello.pdf")
	// Expect both fallback and RFC 5987 filename*.
	if !strings.Contains(got, `filename="hello.pdf"`) {
		t.Errorf("missing ascii fallback: %q", got)
	}
	if !strings.Contains(got, `filename*=UTF-8''hello.pdf`) {
		t.Errorf("missing filename*: %q", got)
	}
}

func TestBuildContentDisposition_NonASCIIRoundTrip(t *testing.T) {
	original := "résumé — cañón.pdf"
	got := buildContentDisposition(original)

	// Parse with mime.ParseMediaType. Go decodes RFC 2231/5987 filename*
	// into params["filename"] — when both are present the RFC 5987 form
	// wins, which is exactly the round-trip we want to prove.
	_, params, err := mime.ParseMediaType(got)
	if err != nil {
		t.Fatalf("ParseMediaType(%q): %v", got, err)
	}
	decoded := params["filename"]
	if decoded != original {
		t.Errorf("filename = %q, want %q (expected RFC 5987 decode)", decoded, original)
	}

	// The legacy ASCII fallback must still be present in the raw header.
	if !strings.Contains(got, `filename="`) {
		t.Errorf("raw header %q missing legacy filename= fallback", got)
	}
	// …and must be CRLF-safe and ASCII-only when stripped of the header key.
	for i := 0; i < len(got); i++ {
		b := got[i]
		if b == '\r' || b == '\n' {
			t.Fatalf("header still contains CRLF: %q", got)
		}
	}
}

func TestBuildContentDisposition_CRLFStripped(t *testing.T) {
	got := buildContentDisposition("evil\r\nX-Injected: 1.txt")
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("header value still contains CRLF: %q", got)
	}
}

func TestBuildContentDisposition_QuoteEscaped(t *testing.T) {
	got := buildContentDisposition(`my"file.pdf`)
	if !strings.Contains(got, `\"`) {
		t.Errorf("expected escaped quote, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Task 5 — Symlink rejection in ValidateFilePathWithin
// ---------------------------------------------------------------------------

func TestValidateFilePathWithin_RejectsSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	// Point outside the allowed root — on darwin/linux /etc/passwd is a
	// universal example of something a server should never surface.
	target := "/etc/passwd"
	linkPath := filepath.Join(root, "escape.lnk")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	_, err := ValidateFilePathWithin(linkPath, root)
	if err == nil {
		t.Fatal("expected symlink-outside-root to be rejected")
	}
	if !errors.Is(err, ErrSymlinkEscape) {
		t.Errorf("err = %v, want ErrSymlinkEscape wrap", err)
	}
}

func TestValidateFilePathWithin_AllowsSymlinkWithinRoot(t *testing.T) {
	root := t.TempDir()
	realPath := filepath.Join(root, "data.txt")
	if err := os.WriteFile(realPath, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(root, "alias.lnk")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	resolved, err := ValidateFilePathWithin(linkPath, root)
	if err != nil {
		t.Fatalf("in-root symlink should be allowed: %v", err)
	}
	// Accept either the lexical form or the macOS /private/var form.
	if !strings.Contains(resolved, "data.txt") {
		t.Errorf("resolved %q does not point at target", resolved)
	}
}

func TestValidateFilePathWithin_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	_, err := ValidateFilePathWithin("../../etc/passwd", root)
	if err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if !errors.Is(err, ErrPathOutsideRoot) {
		t.Errorf("err = %v, want ErrPathOutsideRoot wrap", err)
	}
}

func TestValidateFilePathWithin_EmptyRootRejected(t *testing.T) {
	_, err := ValidateFilePathWithin("foo.txt", "")
	if err == nil {
		t.Fatal("expected error for empty root")
	}
}

func TestValidateFilePathWithin_NonExistentAllowed(t *testing.T) {
	root := t.TempDir()
	// Write target does not exist yet — the helper only enforces
	// lexical containment in that case.
	resolved, err := ValidateFilePathWithin("new-file.bin", root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// On macOS root may be under /var -> /private/var; just require
	// the helper returned something containing the tempdir name.
	if !strings.Contains(resolved, "new-file.bin") {
		t.Errorf("resolved %q does not include target name", resolved)
	}
}

// ---------------------------------------------------------------------------
// Task 6 — ParamInt / ParamInt64 sentinel errors
// ---------------------------------------------------------------------------

func TestParamInt_DistinguishesMissingFromMalformed(t *testing.T) {
	c, _ := NewTestContext("GET", "/")

	// Missing param.
	_, err := c.ParamInt("id")
	if err == nil {
		t.Fatal("expected missing param to error")
	}
	if !errors.Is(err, ErrParamNotFound) {
		t.Errorf("err = %v, want ErrParamNotFound wrap", err)
	}

	// Malformed param present.
	c.params = append(c.params, RouteParam{Key: "id", Value: "abc"})
	_, err = c.ParamInt("id")
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !errors.Is(err, ErrParamParse) {
		t.Errorf("err = %v, want ErrParamParse wrap", err)
	}
	// Must NOT match ErrParamNotFound.
	if errors.Is(err, ErrParamNotFound) {
		t.Error("parse error must not be confused with missing")
	}

	// Valid param.
	c.params = []RouteParam{{Key: "id", Value: "42"}}
	n, err := c.ParamInt("id")
	if err != nil {
		t.Fatal(err)
	}
	if n != 42 {
		t.Errorf("ParamInt = %d, want 42", n)
	}
}

func TestParamInt64_DistinguishesMissingFromMalformed(t *testing.T) {
	c, _ := NewTestContext("GET", "/")

	_, err := c.ParamInt64("id")
	if !errors.Is(err, ErrParamNotFound) {
		t.Errorf("missing: err = %v, want ErrParamNotFound", err)
	}

	c.params = []RouteParam{{Key: "id", Value: "not-a-number"}}
	_, err = c.ParamInt64("id")
	if !errors.Is(err, ErrParamParse) {
		t.Errorf("malformed: err = %v, want ErrParamParse", err)
	}
	if errors.Is(err, ErrParamNotFound) {
		t.Error("parse error must not report ErrParamNotFound")
	}

	c.params = []RouteParam{{Key: "id", Value: "9223372036854775807"}}
	n, err := c.ParamInt64("id")
	if err != nil {
		t.Fatal(err)
	}
	if n != 9223372036854775807 {
		t.Errorf("ParamInt64 = %d", n)
	}
}

// ---------------------------------------------------------------------------
// Static fallback opt-in smoke test (task 4)
// ---------------------------------------------------------------------------

func TestStaticFallback_RoutesWinOverFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "about"), []byte("static"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewV2()
	r.StaticFallback(dir)
	// Route takes precedence under StaticFallback.
	r.Get("/about", func(c *Context) error {
		return c.String(http.StatusOK, "from-route")
	})

	req := httptest.NewRequest("GET", "/about", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if body := rec.Body.String(); !strings.Contains(body, "from-route") {
		t.Errorf("route should have won under StaticFallback, got body %q", body)
	}
}

func TestStaticFallback_FilesServedWhenNoRoute(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("static-body"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewV2()
	r.StaticFallback(dir)

	req := httptest.NewRequest("GET", "/readme.txt", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if body := rec.Body.String(); !strings.Contains(body, "static-body") {
		t.Errorf("static file should have been served, got body %q", body)
	}
}

// ---------------------------------------------------------------------------
// Typed Event sanity check (task 8)
// ---------------------------------------------------------------------------

func TestTypedEvent_OnEventDispatchError(t *testing.T) {
	r := NewV2()
	r.SetEventDispatcher(func(event interface{}) error { return ErrEventBufferFull })

	var seenName string
	r.OnEventDispatchError = func(err error, event Event) {
		if event != nil {
			seenName = event.Name()
		}
	}

	r.dispatchInstanceEvent(&RequestStarted{RequestID: "abc"})

	if seenName != "request.started" {
		t.Errorf("event.Name = %q, want request.started", seenName)
	}
}

// Ensures the redirect sanitizer emits a legitimate URL even for a
// passthrough — catch an accidental nil/empty-string regression.
func TestSanitizeRedirect_PassthroughReturnsURL(t *testing.T) {
	got := sanitizeRedirect("https://trusted.example/x", []string{"trusted.example"})
	u, err := url.Parse(got)
	if err != nil || u.Host != "trusted.example" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

// Smoke-test that the Download path sets Content-Disposition.
func TestDownload_SetsRFC5987Header(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, rec := NewTestContext("GET", "/")
	if err := c.Download(path, "résumé.pdf"); err != nil {
		t.Fatal(err)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "filename*=UTF-8''") {
		t.Errorf("Content-Disposition = %q, want RFC 5987 encoding", cd)
	}
}
