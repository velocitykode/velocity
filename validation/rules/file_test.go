package rules

import (
	"bytes"
	"mime/multipart"
	"testing"
)

// fakeFile is a lightweight FileHeader implementation so tests don't need a
// real multipart form. When content is non-nil, fakeFile also implements
// openable so MimesRule / ImageRule can sniff the bytes.
type fakeFile struct {
	name    string
	size    int64
	content []byte
}

func (f fakeFile) Filename() string { return f.name }
func (f fakeFile) Size() int64      { return f.size }

// Open returns a fresh reader over the file's content. Matches the
// openable interface (Open() (multipart.File, error)).
func (f fakeFile) Open() (multipart.File, error) {
	return &fakeMultipartFile{Reader: bytes.NewReader(f.content)}, nil
}

// fakeMultipartFile adapts a *bytes.Reader to satisfy multipart.File
// (io.Reader, io.ReaderAt, io.Seeker, io.Closer).
type fakeMultipartFile struct {
	*bytes.Reader
}

func (f *fakeMultipartFile) Close() error { return nil }

// pngBytes is a minimal valid PNG header so http.DetectContentType
// returns "image/png".
var pngBytes = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0}

// jpegBytes is a minimal JPEG SOI marker plus JFIF header.
var jpegBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0, 0}

// gifBytes is a GIF89a signature.
var gifBytes = []byte("GIF89a")

func TestFileRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"nil ok", nil, false},
		{"FileHeader interface", fakeFile{name: "x.pdf", size: 10}, false},
		{"*multipart.FileHeader", &multipart.FileHeader{Filename: "x.pdf", Size: 10}, false},
		{"nil *multipart.FileHeader", (*multipart.FileHeader)(nil), true},
		{"string fails", "not-a-file", true},
		{"int fails", 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := FileRule("upload", tt.value, nil, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestMimesRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		params  []string
		wantErr bool
	}{
		{"nil ok", nil, []string{"pdf"}, false},
		{"missing params", fakeFile{name: "x.pdf", content: []byte("%PDF-1.4\n")}, nil, true},
		{
			"matches with valid content",
			fakeFile{name: "report.pdf", content: []byte("%PDF-1.4\n")},
			[]string{"pdf"},
			false,
		},
		{
			"case insensitive",
			fakeFile{name: "PHOTO.JPG", content: jpegBytes},
			[]string{"jpg"},
			false,
		},
		{
			"leading dot in param",
			fakeFile{name: "photo.jpg", content: jpegBytes},
			[]string{".jpg"},
			false,
		},
		{
			"extension mismatches allowlist",
			fakeFile{name: "report.txt", content: []byte("hello")},
			[]string{"pdf"},
			true,
		},
		{"non-file value", "report.pdf", []string{"pdf"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := MimesRule("upload", tt.value, tt.params, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

// TestMimesRule_ContentSniffRejectsExtensionLie ensures that a file
// renamed to a benign extension but carrying executable content is
// rejected by content sniffing. payload.php.jpg with PHP content must
// not pass mimes:jpg. Audit ID H-21.
func TestMimesRule_ContentSniffRejectsExtensionLie(t *testing.T) {
	// PHP content masquerading as a JPEG; payload.php.jpg ends in .jpg
	// so the old filename-only check would accept it.
	phpPayload := []byte("<?php system($_GET['cmd']); ?>\n")
	upload := fakeFile{name: "payload.php.jpg", size: int64(len(phpPayload)), content: phpPayload}

	// Even before sniffing, .php in the filename should be blocked.
	if err := MimesRule("upload", upload, []string{"jpg"}, nil); err == nil {
		t.Fatal("expected payload.php.jpg to be rejected, got nil")
	}

	// Same content under a clean extension still gets caught by the
	// content sniff: http.DetectContentType returns text/plain, not
	// image/jpeg.
	upload2 := fakeFile{name: "payload.jpg", size: int64(len(phpPayload)), content: phpPayload}
	if err := MimesRule("upload", upload2, []string{"jpg"}, nil); err == nil {
		t.Fatal("expected text-content under .jpg name to be rejected, got nil")
	}
}

// TestMimesRule_BlocksScriptExtensions ensures script-runner extensions
// are refused outright regardless of the allowlist. Audit ID H-21.
func TestMimesRule_BlocksScriptExtensions(t *testing.T) {
	cases := []string{
		"shell.sh", "evil.php", "evil.phtml", "evil.phar",
		"evil.cgi", "evil.pl", "evil.py", "evil.rb",
		"x.php.jpg", // double extension
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			upload := fakeFile{name: name, content: jpegBytes}
			// Caller asks for "php" or "sh" or whatever the suffix is;
			// rule must still refuse.
			if err := MimesRule("upload", upload, []string{"php", "sh", "pl", "py", "rb", "cgi", "phtml", "phar", "jpg"}, nil); err == nil {
				t.Fatalf("expected %s to be rejected, got nil", name)
			}
		})
	}
}

func TestImageRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		params  []string
		wantErr bool
	}{
		{"nil ok", nil, nil, false},
		{"png passes", fakeFile{name: "photo.png", content: pngBytes}, nil, false},
		{"jpg passes", fakeFile{name: "photo.jpg", content: jpegBytes}, nil, false},
		{"gif passes", fakeFile{name: "photo.gif", content: gifBytes}, nil, false},
		{"pdf fails", fakeFile{name: "doc.pdf", content: []byte("%PDF-1.4\n")}, nil, true},
		{"non-file fails", "photo.png", nil, true},
		{
			"extension says png but content is text",
			fakeFile{name: "photo.png", content: []byte("not a png")},
			nil,
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ImageRule("photo", tt.value, tt.params, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

// TestImageRule_SVGOptIn verifies that SVG is rejected by default and
// only accepted when image:svg (or image:allow_svg) is set, AND that
// SVG with <script> is rejected even with opt-in. Audit ID H-21.
func TestImageRule_SVGOptIn(t *testing.T) {
	cleanSVG := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><circle cx="50" cy="50" r="40"/></svg>`)
	scriptSVG := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)

	// Default: SVG rejected.
	if err := ImageRule("photo", fakeFile{name: "logo.svg", content: cleanSVG}, nil, nil); err == nil {
		t.Fatal("expected default SVG to be rejected, got nil")
	}
	// Opt-in: clean SVG accepted.
	if err := ImageRule("photo", fakeFile{name: "logo.svg", content: cleanSVG}, []string{"svg"}, nil); err != nil {
		t.Fatalf("expected svg with opt-in to pass, got %v", err)
	}
	if err := ImageRule("photo", fakeFile{name: "logo.svg", content: cleanSVG}, []string{"allow_svg"}, nil); err != nil {
		t.Fatalf("expected svg with allow_svg to pass, got %v", err)
	}
	// Opt-in: SVG carrying <script> still rejected.
	if err := ImageRule("photo", fakeFile{name: "evil.svg", content: scriptSVG}, []string{"svg"}, nil); err == nil {
		t.Fatal("expected scripted SVG to be rejected even with opt-in, got nil")
	}
}

// TestImageRule_RejectsNonOpener verifies a value carrying file metadata
// but no Open method fails closed. Without content, the extension alone
// is untrustworthy.
func TestImageRule_RejectsNonOpener(t *testing.T) {
	hdr := nonOpenerFile{name: "photo.png", size: 10}
	if err := ImageRule("photo", hdr, nil, nil); err == nil {
		t.Fatal("expected non-openable FileHeader to be rejected, got nil")
	}
}

// nonOpenerFile satisfies FileHeader but deliberately does NOT implement
// openable, used by TestImageRule_RejectsNonOpener.
type nonOpenerFile struct {
	name string
	size int64
}

func (n nonOpenerFile) Filename() string { return n.name }
func (n nonOpenerFile) Size() int64      { return n.size }
