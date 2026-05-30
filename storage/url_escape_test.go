package storage

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalURL_EscapesReservedCharacters pins FS-URL-ESCAPE for the
// local-driver emitter. A key containing `?`, `#`, space, and `%` must
// be percent-encoded so the emitted URL parses as a single path with
// no spurious query string or fragment.
func TestLocalURL_EscapesReservedCharacters(t *testing.T) {
	testDir := filepath.Join(os.TempDir(), "velocity-storage-url-escape")
	_ = os.RemoveAll(testDir)
	defer os.RemoveAll(testDir)

	driver := NewLocalDriver(DiskConfig{
		Driver: "local",
		Root:   testDir,
		URL:    "https://cdn.example.com/storage",
	})

	cases := []struct {
		name string
		key  string
	}{
		{"query_char", "my doc?v=1.pdf"},
		{"fragment_char", "my doc#section.pdf"},
		{"both", "my doc?v=1#sec.pdf"},
		{"percent", "100%real.pdf"},
		{"plus", "a+b.txt"},
		{"ampersand", "a&b=c.txt"},
		{"slashes_preserved", "a/b c/d?e.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := driver.URL(tc.key)
			u, err := url.Parse(got)
			if err != nil {
				t.Fatalf("emitted URL %q is unparseable: %v", got, err)
			}
			if u.RawQuery != "" {
				t.Errorf("emitted URL %q has RawQuery=%q; reserved chars leaked", got, u.RawQuery)
			}
			if u.Fragment != "" {
				t.Errorf("emitted URL %q has Fragment=%q; reserved chars leaked", got, u.Fragment)
			}
			// The decoded path component must contain the literal key.
			// Strip the configured base + leading slash before comparing.
			const base = "https://cdn.example.com/storage/"
			if !strings.HasPrefix(got, base) {
				t.Fatalf("emitted URL %q missing expected prefix %q", got, base)
			}
			encodedPath := strings.TrimPrefix(got, base)
			decoded, err := url.PathUnescape(encodedPath)
			if err != nil {
				t.Fatalf("encoded segment %q failed to unescape: %v", encodedPath, err)
			}
			if decoded != tc.key {
				t.Errorf("decoded path %q != original key %q", decoded, tc.key)
			}
			// Slash separators between segments must NOT be escaped.
			if strings.Contains(tc.key, "/") && !strings.Contains(encodedPath, "/") {
				t.Errorf("path %q lost slash separators in %q", tc.key, encodedPath)
			}
		})
	}
}

// TestLocalURL_EmptyBaseStaysEmpty pins the no-URL-configured branch
// after the escape refactor.
func TestLocalURL_EmptyBaseStaysEmpty(t *testing.T) {
	testDir := filepath.Join(os.TempDir(), "velocity-storage-url-empty")
	_ = os.RemoveAll(testDir)
	defer os.RemoveAll(testDir)

	driver := NewLocalDriver(DiskConfig{
		Driver: "local",
		Root:   testDir,
	})
	if got := driver.URL("any/key?v=1"); got != "" {
		t.Errorf("URL with empty base must stay empty, got %q", got)
	}
}

// TestEscapeURLPathSegments_PreservesSlashSeparators is the unit test
// for the helper itself: `/` must survive, every other reserved char
// must be encoded.
func TestEscapeURLPathSegments_PreservesSlashSeparators(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"a", "a"},
		{"a/b", "a/b"},
		{"a b", "a%20b"},
		{"a/b c/d", "a/b%20c/d"},
		{"a?b/c#d", "a%3Fb/c%23d"},
		{"100%real", "100%25real"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := escapeURLPathSegments(tc.in); got != tc.want {
				t.Errorf("escapeURLPathSegments(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
