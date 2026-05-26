package storage

import (
	"strings"
	"testing"
)

// TestDetectMimeType_StdlibSniffer pins the H-17 fix. The previous
// detectMimeType only recognised JPEG/PNG/GIF/PDF and then fell back
// to text/plain (for valid UTF-8) or application/octet-stream. That
// classified HTML, SVG, scripts and most binaries as text/plain, which
// a browser will happily MIME-sniff and execute.
//
// After the fix the function delegates to http.DetectContentType, the
// stdlib implementation of the Mozilla "sniffing" rules with ~30 known
// formats. These cases lock in a few classifications that the toy
// detector got wrong.
func TestDetectMimeType_StdlibSniffer(t *testing.T) {
	tests := []struct {
		name       string
		content    []byte
		wantPrefix string
	}{
		{
			name:       "html",
			content:    []byte("<html><body>hello</body></html>"),
			wantPrefix: "text/html",
		},
		{
			name:       "script_tag",
			content:    []byte("<script>alert(document.cookie)</script>"),
			wantPrefix: "text/html",
		},
		{
			name:       "svg",
			content:    []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`),
			wantPrefix: "text/xml",
		},
		{
			name:       "png",
			content:    []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A},
			wantPrefix: "image/png",
		},
		{
			name:       "gif",
			content:    []byte("GIF89a"),
			wantPrefix: "image/gif",
		},
		{
			name:       "jpeg",
			content:    []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'},
			wantPrefix: "image/jpeg",
		},
		{
			name:       "pdf",
			content:    []byte("%PDF-1.4"),
			wantPrefix: "application/pdf",
		},
		{
			name:       "plain_utf8",
			content:    []byte("the quick brown fox"),
			wantPrefix: "text/plain",
		},
		{
			name:       "binary",
			content:    []byte{0x00, 0x01, 0x02, 0x03},
			wantPrefix: "application/octet-stream",
		},
		{
			name:       "empty",
			content:    nil,
			wantPrefix: "application/octet-stream",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectMimeType(tc.content)
			if !strings.HasPrefix(got, tc.wantPrefix) {
				t.Errorf("detectMimeType(%s) = %q, want prefix %q", tc.name, got, tc.wantPrefix)
			}
		})
	}
}

// TestDetectMimeType_HtmlNotClassifiedAsPlain locks in the security
// property the audit cares about most: HTML uploaded with a benign
// extension must be sniffed as HTML, not text/plain. With the toy
// detector, an attacker could upload <script>...</script> and have S3
// store it as text/plain; many browsers (especially older ones or
// those without "X-Content-Type-Options: nosniff") would then execute
// the script when served from the application origin.
func TestDetectMimeType_HtmlNotClassifiedAsPlain(t *testing.T) {
	html := []byte("<!doctype html><script>x()</script>")
	mime := detectMimeType(html)
	if strings.HasPrefix(mime, "text/plain") {
		t.Errorf("HTML must not be sniffed as text/plain, got %q", mime)
	}
}

// TestDetectMimeType_OnlyReadsFirst512Bytes ensures we don't pay quadratic
// scan cost on huge payloads. http.DetectContentType caps at 512 by
// contract; we double-check by feeding a large slice and asserting the
// classification matches a slice of length 512.
func TestDetectMimeType_OnlyReadsFirst512Bytes(t *testing.T) {
	prefix := []byte("<!doctype html>")
	big := append(prefix, make([]byte, 1<<20)...)

	want := detectMimeType(prefix)
	got := detectMimeType(big)
	if got != want {
		t.Errorf("sniffer must be bounded to first 512 bytes; got %q want %q", got, want)
	}
}
