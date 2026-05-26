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

// TestMimesRule_SVGRequiresOptIn verifies the F1 fix: mimes:svg without
// an "allow_svg" opt-in is refused, and even with opt-in a scripted SVG
// is rejected. Without this fix, scripted SVG flowed through mimes:svg
// straight to disk while ImageRule was the only path scanning for
// <script. Audit ID F1.
func TestMimesRule_SVGRequiresOptIn(t *testing.T) {
	cleanSVG := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><circle cx="50" cy="50" r="40"/></svg>`)
	scriptSVG := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)

	// mimes:svg without opt-in: refused even for clean SVG.
	if err := MimesRule("upload", fakeFile{name: "logo.svg", content: cleanSVG}, []string{"svg"}, nil); err == nil {
		t.Fatal("expected mimes:svg without allow_svg to refuse clean SVG, got nil")
	}

	// mimes:svg,allow_svg: clean SVG accepted.
	if err := MimesRule("upload", fakeFile{name: "logo.svg", content: cleanSVG}, []string{"svg", "allow_svg"}, nil); err != nil {
		t.Fatalf("expected mimes:svg,allow_svg to accept clean SVG, got %v", err)
	}

	// mimes:jpg,svg,allow_svg: mixed allowlist still accepts clean SVG.
	if err := MimesRule("upload", fakeFile{name: "logo.svg", content: cleanSVG}, []string{"jpg", "svg", "allow_svg"}, nil); err != nil {
		t.Fatalf("expected mimes:jpg,svg,allow_svg to accept clean SVG, got %v", err)
	}

	// mimes:svg,allow_svg with scripted SVG: refused. This is the
	// regression that motivated F1; before the fix the request would
	// pass since MimesRule never ran the script scan.
	if err := MimesRule("upload", fakeFile{name: "evil.svg", content: scriptSVG}, []string{"svg", "allow_svg"}, nil); err == nil {
		t.Fatal("expected mimes:svg,allow_svg to refuse scripted SVG, got nil")
	}

	// Sibling check: mimes:jpg with a scripted SVG (renamed to .svg) is
	// refused twice over: not in the allowlist, and would be refused by
	// the SVG script scan even if it were.
	if err := MimesRule("upload", fakeFile{name: "evil.svg", content: scriptSVG}, []string{"jpg"}, nil); err == nil {
		t.Fatal("expected mimes:jpg with .svg upload to be refused, got nil")
	}
}

// TestMimesRule_SVGOptInScopedToActualUpload verifies F4: the
// allow_svg opt-in only governs uploads whose own extension is .svg.
// Including "svg" in the mimes allowlist must not block other valid
// uploads like .jpg. Audit ID F4.
func TestMimesRule_SVGOptInScopedToActualUpload(t *testing.T) {
	cleanSVG := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><circle cx="50" cy="50" r="40"/></svg>`)
	scriptSVG := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)

	// mimes:jpg,svg (no opt-in) on clean .jpg: ACCEPTED. Before F4 this
	// was rejected because the allowlist contained "svg" and the
	// allowlist-wide gate fired before looking at the actual upload.
	if err := MimesRule("upload", fakeFile{name: "photo.jpg", content: jpegBytes}, []string{"jpg", "svg"}, nil); err != nil {
		t.Fatalf("expected mimes:jpg,svg to accept clean .jpg without opt-in, got %v", err)
	}

	// mimes:jpg,svg (no opt-in) on clean .svg: REJECTED via SVG
	// opt-in requirement.
	if err := MimesRule("upload", fakeFile{name: "logo.svg", content: cleanSVG}, []string{"jpg", "svg"}, nil); err == nil {
		t.Fatal("expected mimes:jpg,svg without opt-in to refuse clean .svg upload, got nil")
	}

	// mimes:jpg,svg,allow_svg on clean .svg: ACCEPTED.
	if err := MimesRule("upload", fakeFile{name: "logo.svg", content: cleanSVG}, []string{"jpg", "svg", "allow_svg"}, nil); err != nil {
		t.Fatalf("expected mimes:jpg,svg,allow_svg to accept clean .svg, got %v", err)
	}

	// mimes:jpg,svg,allow_svg on scripted .svg: REJECTED via validateSVG.
	if err := MimesRule("upload", fakeFile{name: "evil.svg", content: scriptSVG}, []string{"jpg", "svg", "allow_svg"}, nil); err == nil {
		t.Fatal("expected mimes:jpg,svg,allow_svg to refuse scripted .svg, got nil")
	}

	// Other non-SVG uploads under an svg-containing allowlist must
	// pass cleanly when their own type matches.
	if err := MimesRule("upload", fakeFile{name: "photo.png", content: pngBytes}, []string{"png", "svg"}, nil); err != nil {
		t.Fatalf("expected mimes:png,svg to accept clean .png without opt-in, got %v", err)
	}
}

// TestMimesRule_BusinessFormatsAccepted verifies F3: extensions in the
// common-business set (docx, xlsx, pptx, wasm, rar, 7z, doc/xls/ppt,
// rtf, yaml, ...) are no longer rejected as "unsupported file type".
// Each format passes when given content whose sniffed MIME falls into
// its accepted set. Audit ID F3.
func TestMimesRule_BusinessFormatsAccepted(t *testing.T) {
	// Minimal ZIP container header used by OOXML / OpenDocument.
	zipBytes := []byte{0x50, 0x4B, 0x03, 0x04, 0x14, 0x00, 0x00, 0x00, 0x08, 0x00, 0, 0, 0, 0}
	// WebAssembly magic + version.
	wasmBytes := []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00, 0, 0, 0, 0}
	// RAR v5 magic.
	rarBytes := []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x01, 0x00, 0, 0, 0, 0}
	// OLE compound document (legacy doc/xls/ppt).
	oleBytes := []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1, 0, 0, 0, 0}
	// 7z magic.
	sevenZBytes := []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C, 0, 0, 0, 0}
	// tar v7 header with ustar magic at offset 257.
	tarBytes := make([]byte, 512)
	copy(tarBytes[257:], []byte("ustar  \x00"))
	// RTF starts with "{\rtf".
	rtfBytes := []byte("{\\rtf1\\ansi hello}")
	// YAML / YML sniff as text/plain.
	yamlBytes := []byte("key: value\nlist:\n  - one\n  - two\n")
	tsvBytes := []byte("a\tb\tc\n1\t2\t3\n")

	cases := []struct {
		name    string
		file    fakeFile
		params  []string
		wantErr bool
	}{
		{"docx accepted as zip", fakeFile{name: "report.docx", content: zipBytes}, []string{"docx"}, false},
		{"xlsx accepted as zip", fakeFile{name: "sheet.xlsx", content: zipBytes}, []string{"xlsx"}, false},
		{"pptx accepted as zip", fakeFile{name: "deck.pptx", content: zipBytes}, []string{"pptx"}, false},
		{"odt accepted as zip", fakeFile{name: "doc.odt", content: zipBytes}, []string{"odt"}, false},
		{"wasm accepted", fakeFile{name: "mod.wasm", content: wasmBytes}, []string{"wasm"}, false},
		{"rar accepted", fakeFile{name: "archive.rar", content: rarBytes}, []string{"rar"}, false},
		{"7z accepted", fakeFile{name: "archive.7z", content: sevenZBytes}, []string{"7z"}, false},
		{"tar accepted", fakeFile{name: "bundle.tar", content: tarBytes}, []string{"tar"}, false},
		{"doc accepted (OLE)", fakeFile{name: "memo.doc", content: oleBytes}, []string{"doc"}, false},
		{"xls accepted (OLE)", fakeFile{name: "data.xls", content: oleBytes}, []string{"xls"}, false},
		{"ppt accepted (OLE)", fakeFile{name: "slides.ppt", content: oleBytes}, []string{"ppt"}, false},
		{"rtf accepted", fakeFile{name: "notes.rtf", content: rtfBytes}, []string{"rtf"}, false},
		{"yaml accepted", fakeFile{name: "config.yaml", content: yamlBytes}, []string{"yaml"}, false},
		{"yml accepted", fakeFile{name: "config.yml", content: yamlBytes}, []string{"yml"}, false},
		{"tsv accepted", fakeFile{name: "data.tsv", content: tsvBytes}, []string{"tsv"}, false},
		{
			"mixed allowlist accepts docx",
			fakeFile{name: "report.docx", content: zipBytes},
			[]string{"pdf", "docx", "xlsx"},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := MimesRule("upload", tc.file, tc.params, nil)
			if (err != nil) != tc.wantErr {
				t.Fatalf("want err=%v, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestMimesRule_JARRefused verifies F6: .jar is no longer in the
// mimes allowlist and is also in the executable extension blocklist.
// A JAR file is a ZIP of executable Java bytecode; the framework's
// stated posture refuses executable uploads through mimes. Audit ID F6.
func TestMimesRule_JARRefused(t *testing.T) {
	// JAR / ZIP container header (PK\x03\x04).
	jarBytes := []byte{0x50, 0x4B, 0x03, 0x04, 0x14, 0x00, 0x00, 0x00, 0x08, 0x00, 0, 0, 0, 0}
	plainZip := []byte{0x50, 0x4B, 0x03, 0x04, 0x14, 0x00, 0x00, 0x00, 0x08, 0x00, 0, 0, 0, 0}
	wasmBytes := []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00, 0, 0, 0, 0}

	// mimes:jar on a real JAR: refused. The "jar" token is no longer
	// in extMimeAllowlist, and the .jar extension itself is in the
	// blockedExecutableExts list.
	if err := MimesRule("upload", fakeFile{name: "app.jar", content: jarBytes}, []string{"jar"}, nil); err == nil {
		t.Fatal("expected mimes:jar on real JAR to be refused, got nil")
	}

	// mimes:zip on a file named .jar: refused via the executable
	// extension blocklist (the .jar segment scan catches it before
	// reaching the allowlist).
	if err := MimesRule("upload", fakeFile{name: "app.jar", content: jarBytes}, []string{"zip"}, nil); err == nil {
		t.Fatal("expected mimes:zip on .jar-named file to be refused, got nil")
	}

	// mimes:zip on a rename-trick app.jar.zip: refused via the
	// segment scan that catches .jar anywhere in the filename.
	if err := MimesRule("upload", fakeFile{name: "app.jar.zip", content: jarBytes}, []string{"zip"}, nil); err == nil {
		t.Fatal("expected mimes:zip on app.jar.zip to be refused, got nil")
	}

	// mimes:zip on plain .zip with benign ZIP contents: still ACCEPTED.
	// This documents the limitation that ZIP entries are not scanned;
	// the package doc spells this out.
	if err := MimesRule("upload", fakeFile{name: "archive.zip", content: plainZip}, []string{"zip"}, nil); err != nil {
		t.Fatalf("expected mimes:zip on plain .zip to be accepted, got %v", err)
	}

	// Sanity regression: mimes:wasm on real WASM is unaffected.
	if err := MimesRule("upload", fakeFile{name: "mod.wasm", content: wasmBytes}, []string{"wasm"}, nil); err != nil {
		t.Fatalf("expected mimes:wasm on real WASM to be accepted, got %v", err)
	}
}

// TestMimesRule_ContentExecutableRefused verifies F5: a native
// executable payload (PE/ELF/Mach-O/Java .class) is refused even when
// the upload uses a benign extension that the allowlist accepts via
// the application/octet-stream fallback. Before F5, a PE renamed
// report.doc passed mimes:doc since the OLE doc entry accepts
// application/octet-stream and the executable blocklist only looked
// at the filename suffix. Audit ID F5.
func TestMimesRule_ContentExecutableRefused(t *testing.T) {
	// PE/COFF: MZ at offset 0.
	pe := []byte{0x4D, 0x5A, 0x90, 0x00, 0x03, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00}
	// ELF: \x7fELF at offset 0.
	elf := []byte{0x7F, 0x45, 0x4C, 0x46, 0x02, 0x01, 0x01, 0x00, 0, 0, 0, 0}
	// Mach-O 64-bit LE.
	machoLE := []byte{0xCF, 0xFA, 0xED, 0xFE, 0x07, 0x00, 0x00, 0x01, 0, 0, 0, 0}
	// Mach-O 64-bit BE.
	machoBE := []byte{0xFE, 0xED, 0xFA, 0xCF, 0x00, 0x00, 0x00, 0x07, 0, 0, 0, 0}
	// Mach-O 32-bit LE.
	macho32LE := []byte{0xCE, 0xFA, 0xED, 0xFE, 0x07, 0x00, 0x00, 0x00, 0, 0, 0, 0}
	// Mach-O fat / Java .class shared magic.
	classMagic := []byte{0xCA, 0xFE, 0xBA, 0xBE, 0x00, 0x00, 0x00, 0x34, 0, 0, 0, 0}

	cases := []struct {
		name   string
		file   fakeFile
		params []string
	}{
		{"PE renamed report.doc", fakeFile{name: "report.doc", content: pe}, []string{"doc"}},
		{"ELF renamed clip.mp4", fakeFile{name: "clip.mp4", content: elf}, []string{"mp4"}},
		{"Mach-O 64 LE renamed archive.7z", fakeFile{name: "archive.7z", content: machoLE}, []string{"7z"}},
		{"Mach-O 64 BE renamed archive.tar", fakeFile{name: "archive.tar", content: machoBE}, []string{"tar"}},
		{"Mach-O 32 LE renamed bundle.rar", fakeFile{name: "bundle.rar", content: macho32LE}, []string{"rar"}},
		{"Java class renamed image.tif", fakeFile{name: "image.tif", content: classMagic}, []string{"tif"}},
		{"PE under any mimes call", fakeFile{name: "thing.pdf", content: pe}, []string{"pdf"}},
		{"ELF under mimes:wasm fallback", fakeFile{name: "mod.wasm", content: elf}, []string{"wasm"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := MimesRule("upload", tc.file, tc.params, nil)
			if err == nil {
				t.Fatalf("expected %s to be refused as executable content, got nil", tc.name)
			}
		})
	}
}

// TestMimesRule_LegitimateBinaryFormatsStillAccepted asserts F5's
// executable check does not over-reject benign binary formats whose
// magic bytes happen to share leading bytes with executables. Real
// OLE compound documents, real tar archives, real wasm modules, and
// real RAR archives must continue to pass after F5. Audit ID F5.
func TestMimesRule_LegitimateBinaryFormatsStillAccepted(t *testing.T) {
	// OLE compound (real doc/xls/ppt) magic.
	ole := []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1, 0, 0, 0, 0}
	// tar v7 with ustar magic at offset 257.
	tarBytes := make([]byte, 512)
	copy(tarBytes[257:], []byte("ustar  \x00"))
	// WebAssembly magic.
	wasm := []byte{0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00, 0, 0, 0, 0}
	// RAR v5 magic.
	rar := []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x01, 0x00, 0, 0, 0, 0}
	// 7z magic.
	sevenZ := []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C, 0, 0, 0, 0}

	cases := []struct {
		name   string
		file   fakeFile
		params []string
	}{
		{"real OLE .doc", fakeFile{name: "memo.doc", content: ole}, []string{"doc"}},
		{"real tar", fakeFile{name: "bundle.tar", content: tarBytes}, []string{"tar"}},
		{"real wasm", fakeFile{name: "mod.wasm", content: wasm}, []string{"wasm"}},
		{"real RAR", fakeFile{name: "archive.rar", content: rar}, []string{"rar"}},
		{"real 7z", fakeFile{name: "archive.7z", content: sevenZ}, []string{"7z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := MimesRule("upload", tc.file, tc.params, nil)
			if err != nil {
				t.Fatalf("expected %s to pass after F5, got %v", tc.name, err)
			}
		})
	}
}

// TestIsExecutableContent unit-tests the magic-byte detector
// independently of MimesRule. Audit ID F5.
func TestIsExecutableContent(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		want bool
	}{
		{"PE", []byte{0x4D, 0x5A, 0x90, 0x00}, true},
		{"ELF", []byte{0x7F, 0x45, 0x4C, 0x46}, true},
		{"Mach-O 32 BE", []byte{0xFE, 0xED, 0xFA, 0xCE}, true},
		{"Mach-O 32 LE", []byte{0xCE, 0xFA, 0xED, 0xFE}, true},
		{"Mach-O 64 BE", []byte{0xFE, 0xED, 0xFA, 0xCF}, true},
		{"Mach-O 64 LE", []byte{0xCF, 0xFA, 0xED, 0xFE}, true},
		{"fat Mach-O / class", []byte{0xCA, 0xFE, 0xBA, 0xBE}, true},
		{"ZIP", []byte{0x50, 0x4B, 0x03, 0x04}, false},
		{"JPEG", []byte{0xFF, 0xD8, 0xFF, 0xE0}, false},
		{"PNG", []byte{0x89, 0x50, 0x4E, 0x47}, false},
		{"OLE", []byte{0xD0, 0xCF, 0x11, 0xE0}, false},
		{"WASM", []byte{0x00, 0x61, 0x73, 0x6D}, false},
		{"too short", []byte{0x4D, 0x5A}, true}, // MZ alone still PE
		{"empty", []byte{}, false},
		{"one byte", []byte{0x4D}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isExecutableContent(tc.head)
			if got != tc.want {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

// TestMimesRule_ExecutablesStillRefused asserts F3 did not relax the
// executable blocklist. .exe and friends must still be refused even if
// the caller explicitly includes them in the allowlist. Audit ID F3.
func TestMimesRule_ExecutablesStillRefused(t *testing.T) {
	// MZ header for a PE/COFF executable.
	exeBytes := []byte{0x4D, 0x5A, 0x90, 0x00, 0x03, 0x00, 0x00, 0x00}
	bashBytes := []byte("#!/bin/bash\necho hi\n")

	cases := []struct {
		name string
		file fakeFile
	}{
		{"exe refused", fakeFile{name: "evil.exe", content: exeBytes}},
		{"sh refused", fakeFile{name: "evil.sh", content: bashBytes}},
		{"bat refused", fakeFile{name: "evil.bat", content: []byte("echo hi\n")}},
		{"cmd refused", fakeFile{name: "evil.cmd", content: []byte("echo hi\n")}},
		{"php refused", fakeFile{name: "evil.php", content: []byte("<?php echo 1; ?>")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Caller asks for the suffix explicitly; rule must still
			// refuse via the blocklist.
			err := MimesRule("upload", tc.file, []string{"exe", "sh", "bat", "cmd", "php"}, nil)
			if err == nil {
				t.Fatalf("expected %s to be refused, got nil", tc.file.name)
			}
		})
	}
}

// TestValidateSVG_FailsClosedAboveCap verifies F2: a padded SVG that
// pushes the <script> tag past the 1 MiB scan cap must be refused. Under
// the prior implementation the scanner truncated at 1 MiB, missing the
// payload and returning nil. Now the helper refuses outright when the
// file exceeds the cap. Audit ID F2.
func TestValidateSVG_FailsClosedAboveCap(t *testing.T) {
	var sb bytes.Buffer
	sb.WriteString(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg">`)
	// Pad with 1.5 MiB of harmless XML comments so the actual <script
	// tag lands well past svgScanCap (1 MiB).
	const padBytes = 1572864 // 1.5 MiB
	pad := bytes.Repeat([]byte("<!--pad-->"), padBytes/len("<!--pad-->"))
	sb.Write(pad)
	sb.WriteString(`<script>alert(1)</script></svg>`)
	padded := sb.Bytes()

	// Direct helper check.
	if err := validateSVG(fakeFile{name: "big.svg", content: padded}); err == nil {
		t.Fatal("validateSVG accepted oversized SVG; payload would slip past 1 MiB scan cap")
	}

	// Through ImageRule with opt-in.
	if err := ImageRule("photo", fakeFile{name: "big.svg", content: padded}, []string{"svg"}, nil); err == nil {
		t.Fatal("ImageRule accepted oversized SVG with opt-in; expected refusal")
	}

	// Through MimesRule with opt-in.
	if err := MimesRule("upload", fakeFile{name: "big.svg", content: padded}, []string{"svg", "allow_svg"}, nil); err == nil {
		t.Fatal("MimesRule accepted oversized SVG with opt-in; expected refusal")
	}

	// Sanity: an oversized but clean SVG (no script) is also refused.
	// The cap is fail-closed; we cannot prove the absence of <script
	// past the scan window, so we refuse.
	var clean bytes.Buffer
	clean.WriteString(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg">`)
	clean.Write(pad)
	clean.WriteString(`</svg>`)
	if err := validateSVG(fakeFile{name: "big.svg", content: clean.Bytes()}); err == nil {
		t.Fatal("validateSVG accepted oversized clean SVG; expected fail-closed refusal")
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
