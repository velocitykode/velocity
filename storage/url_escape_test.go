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

// TestS3URL_EscapesReservedCharacters_CustomURL pins FS-URL-ESCAPE for
// the S3 driver custom-URL branch.
func TestS3URL_EscapesReservedCharacters_CustomURL(t *testing.T) {
	driver := &S3Driver{
		bucket: "test-bucket",
		region: "us-east-1",
		url:    "https://cdn.example.com/assets",
	}

	keys := []string{
		"my doc?v=1.pdf",
		"my doc#section.pdf",
		"my doc?v=1#sec.pdf",
		"100%real.pdf",
		"folder/a b/file?q=1.txt",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			got := driver.URL(key)
			u, err := url.Parse(got)
			if err != nil {
				t.Fatalf("emitted URL %q is unparseable: %v", got, err)
			}
			if u.RawQuery != "" {
				t.Errorf("URL %q has RawQuery=%q; reserved chars leaked", got, u.RawQuery)
			}
			if u.Fragment != "" {
				t.Errorf("URL %q has Fragment=%q; reserved chars leaked", got, u.Fragment)
			}
			const base = "https://cdn.example.com/assets/"
			if !strings.HasPrefix(got, base) {
				t.Fatalf("URL %q missing prefix %q", got, base)
			}
			decoded, err := url.PathUnescape(strings.TrimPrefix(got, base))
			if err != nil {
				t.Fatalf("encoded segment failed to unescape: %v", err)
			}
			if decoded != key {
				t.Errorf("decoded %q != original %q", decoded, key)
			}
		})
	}
}

// TestS3URL_EscapesReservedCharacters_SynthesisedURL pins FS-URL-ESCAPE
// for the synthesised `s3.<region>.amazonaws.com` branch.
func TestS3URL_EscapesReservedCharacters_SynthesisedURL(t *testing.T) {
	driver := &S3Driver{
		bucket: "test-bucket",
		region: "us-east-1",
		url:    "",
	}

	got := driver.URL("my doc?v=1#sec.pdf")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("emitted URL %q is unparseable: %v", got, err)
	}
	if u.RawQuery != "" {
		t.Errorf("URL %q has RawQuery=%q; reserved chars leaked", got, u.RawQuery)
	}
	if u.Fragment != "" {
		t.Errorf("URL %q has Fragment=%q; reserved chars leaked", got, u.Fragment)
	}
	wantPrefix := "https://test-bucket.s3.us-east-1.amazonaws.com/"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("URL %q missing prefix %q", got, wantPrefix)
	}
	decoded, err := url.PathUnescape(strings.TrimPrefix(got, wantPrefix))
	if err != nil {
		t.Fatalf("encoded segment failed to unescape: %v", err)
	}
	if decoded != "my doc?v=1#sec.pdf" {
		t.Errorf("decoded %q != original", decoded)
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
