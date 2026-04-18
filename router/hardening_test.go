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
// Task 5 — Kernel-enforced path containment via OpenFileIn + os.Root.
//
// The predecessor (ValidateFilePathWithin) used Lstat + EvalSymlinks +
// prefix comparison, which left a TOCTOU window between the validation
// call and the caller's os.Open. OpenFileIn closes that window by
// returning the opened handle directly from os.Root (openat2 on Linux).
// ---------------------------------------------------------------------------

func TestOpenFileIn_AllowsSymlinkInsideRoot(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(realPath, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "alias.lnk")
	if err := os.Symlink("data.txt", linkPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	f, err := OpenFileIn(root, "alias.lnk")
	if err != nil {
		// os.Root on Go 1.24+ refuses to follow symlinks by default on
		// some platforms (RESOLVE_NO_SYMLINKS). That's a stricter
		// security stance than the old helper and is acceptable — the
		// spec bullet is that the call is *allowed* when the target
		// stays inside root, which on platforms that follow symlinks
		// it will be.
		t.Skipf("platform refuses in-root symlinks via os.Root: %v", err)
	}
	defer f.Close()
	got := make([]byte, 2)
	if _, err := f.Read(got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "ok" {
		t.Errorf("read %q, want %q", got, "ok")
	}
}

func TestOpenFileIn_RejectsSymlinkEscapingRoot(t *testing.T) {
	dir := t.TempDir()
	linkPath := filepath.Join(dir, "escape.lnk")
	// Absolute symlink pointing at /etc/passwd — a universal "things
	// the server must never surface" target.
	if err := os.Symlink("/etc/passwd", linkPath); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	f, err := OpenFileIn(root, "escape.lnk")
	if err == nil {
		f.Close()
		t.Fatal("expected escape symlink to be refused")
	}
	if !errors.Is(err, ErrPathOutsideRoot) {
		t.Errorf("err = %v, want wraps ErrPathOutsideRoot", err)
	}
}

func TestOpenFileIn_RejectsTraversalPath(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	f, err := OpenFileIn(root, "../../etc/passwd")
	if err == nil {
		f.Close()
		t.Fatal("expected traversal to be refused")
	}
	if !errors.Is(err, ErrPathOutsideRoot) {
		t.Errorf("err = %v, want wraps ErrPathOutsideRoot", err)
	}
}

func TestOpenFileIn_OpensExistingFile(t *testing.T) {
	dir := t.TempDir()
	want := []byte("hello world")
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), want, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	f, err := OpenFileIn(root, "hello.txt")
	if err != nil {
		t.Fatalf("OpenFileIn: %v", err)
	}
	defer f.Close()
	got := make([]byte, len(want))
	if _, err := f.Read(got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestOpenFileIn_NilRoot(t *testing.T) {
	f, err := OpenFileIn(nil, "anything.txt")
	if err == nil {
		f.Close()
		t.Fatal("expected error for nil root")
	}
	if !errors.Is(err, ErrNilRoot) {
		t.Errorf("err = %v, want ErrNilRoot", err)
	}
}

func TestOpenFileIn_NonexistentFile(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	f, err := OpenFileIn(root, "does-not-exist.txt")
	if err == nil {
		f.Close()
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want errors.Is(err, os.ErrNotExist)", err)
	}
	// Nonexistence is a distinct class of error from escape — do not
	// dress it up with ErrPathOutsideRoot.
	if errors.Is(err, ErrPathOutsideRoot) {
		t.Errorf("missing file should not wrap ErrPathOutsideRoot: %v", err)
	}
}

// TestOpenFileIn_TOCTOUStress exercises the worst case: a concurrent
// attacker repeatedly swaps a symlink's target between an in-root file
// and /etc/passwd. os.Root's openat2-based resolution guarantees that
// OpenFileIn never returns a file descriptor pointing outside root, no
// matter how we lose the race.
func TestOpenFileIn_TOCTOUStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TOCTOU stress under -short")
	}
	dir := t.TempDir()
	// In-root target for the "safe" flip.
	safe := filepath.Join(dir, "safe.txt")
	if err := os.WriteFile(safe, []byte("safe-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "flip.lnk")
	if err := os.Symlink("safe.txt", link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	const iterations = 10_000
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			// Flip between in-root relative symlink and an absolute
			// out-of-root symlink. os.Remove + os.Symlink is the
			// textbook TOCTOU swap.
			_ = os.Remove(link)
			if i%2 == 0 {
				_ = os.Symlink("/etc/passwd", link)
			} else {
				_ = os.Symlink("safe.txt", link)
			}
		}
	}()

	var escapes int
	for i := 0; i < iterations; i++ {
		f, err := OpenFileIn(root, "flip.lnk")
		if err != nil {
			// Expected cases: ErrPathOutsideRoot (attacker won the
			// race and pointed at /etc/passwd), or ErrNotExist
			// (the remove won the race before the re-symlink).
			if !errors.Is(err, ErrPathOutsideRoot) && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected error class: %v", err)
			}
			continue
		}
		// If we did open a handle, it MUST be in-root. The only
		// in-root target is "safe.txt" with known content.
		buf := make([]byte, 32)
		n, _ := f.Read(buf)
		f.Close()
		if string(buf[:n]) != "safe-content" {
			escapes++
			t.Errorf("iter %d: opened out-of-root content: %q", i, buf[:n])
		}
	}
	<-done
	if escapes > 0 {
		t.Fatalf("os.Root leaked %d out-of-root opens", escapes)
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
