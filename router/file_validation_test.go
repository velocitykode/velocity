package router

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

// createTestFileHeader builds a multipart.FileHeader for testing.
func createTestFileHeader(t *testing.T, filename string, content []byte) *multipart.FileHeader {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	req, err := http.NewRequest("POST", "/", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if err := req.ParseMultipartForm(32 << 20); err != nil {
		t.Fatal(err)
	}
	fh := req.MultipartForm.File["file"][0]
	return fh
}

func TestValidateFile_MaxFileSize_Pass(t *testing.T) {
	fh := createTestFileHeader(t, "small.txt", []byte("hello"))
	c, _ := NewTestContext("POST", "/", nil)

	if err := c.ValidateFile(fh, MaxFileSize(1024)); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidateFile_MaxFileSize_Fail(t *testing.T) {
	fh := createTestFileHeader(t, "big.txt", bytes.Repeat([]byte("a"), 2000))
	c, _ := NewTestContext("POST", "/", nil)

	err := c.ValidateFile(fh, MaxFileSize(100))
	if err == nil {
		t.Fatal("expected error for oversized file")
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateFile_AllowedExtensions_Pass(t *testing.T) {
	fh := createTestFileHeader(t, "photo.jpg", []byte("data"))
	c, _ := NewTestContext("POST", "/", nil)

	if err := c.ValidateFile(fh, AllowedExtensions(".jpg", ".png")); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidateFile_AllowedExtensions_CaseInsensitive(t *testing.T) {
	fh := createTestFileHeader(t, "photo.JPG", []byte("data"))
	c, _ := NewTestContext("POST", "/", nil)

	if err := c.ValidateFile(fh, AllowedExtensions(".jpg", ".png")); err != nil {
		t.Errorf("expected no error for case-insensitive match, got: %v", err)
	}
}

func TestValidateFile_AllowedExtensions_Fail(t *testing.T) {
	fh := createTestFileHeader(t, "script.exe", []byte("data"))
	c, _ := NewTestContext("POST", "/", nil)

	err := c.ValidateFile(fh, AllowedExtensions(".jpg", ".png"))
	if err == nil {
		t.Fatal("expected error for disallowed extension")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateFile_AllowedMIMETypes_Pass(t *testing.T) {
	// Plain text content will be detected as "text/plain; charset=utf-8"
	// by http.DetectContentType. Passing just "text/plain" verifies that
	// parameter stripping works correctly during comparison.
	fh := createTestFileHeader(t, "file.txt", []byte("Hello, world!"))
	c, _ := NewTestContext("POST", "/", nil)

	if err := c.ValidateFile(fh, AllowedMIMETypes("text/plain")); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidateFile_AllowedMIMETypes_WithParams(t *testing.T) {
	// Passing "text/plain; charset=utf-8" should also match since params are stripped
	fh := createTestFileHeader(t, "file.txt", []byte("Hello, world!"))
	c, _ := NewTestContext("POST", "/", nil)

	if err := c.ValidateFile(fh, AllowedMIMETypes("text/plain; charset=utf-8")); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidateFile_AllowedMIMETypes_Fail(t *testing.T) {
	// Plain text content detected as text/plain, but we only allow image/jpeg
	fh := createTestFileHeader(t, "file.txt", []byte("Hello, world!"))
	c, _ := NewTestContext("POST", "/", nil)

	err := c.ValidateFile(fh, AllowedMIMETypes("image/jpeg"))
	if err == nil {
		t.Fatal("expected error for disallowed MIME type")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateFile_MultipleOptions(t *testing.T) {
	fh := createTestFileHeader(t, "photo.jpg", []byte("data"))
	c, _ := NewTestContext("POST", "/", nil)

	// Size OK, extension OK
	if err := c.ValidateFile(fh, MaxFileSize(1024), AllowedExtensions(".jpg")); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	// Size too small
	err := c.ValidateFile(fh, MaxFileSize(1), AllowedExtensions(".jpg"))
	if err == nil {
		t.Fatal("expected size error")
	}
}

func TestValidateFile_NoOptions(t *testing.T) {
	fh := createTestFileHeader(t, "anything.xyz", bytes.Repeat([]byte("x"), 5000))
	c, _ := NewTestContext("POST", "/", nil)

	if err := c.ValidateFile(fh); err != nil {
		t.Errorf("no options should pass any file, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SanitizeFilename tests
// ---------------------------------------------------------------------------

func TestSanitizeFilename_Basic(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple filename",
			input:    "photo.jpg",
			expected: "photo.jpg",
		},
		{
			name:     "strips directory",
			input:    "/var/uploads/evil.txt",
			expected: "evil.txt",
		},
		{
			name:     "strips windows-style backslashes",
			input:    `C:\Users\evil.txt`,
			expected: "C__Users_evil.txt",
		},
		{
			name:     "removes null bytes",
			input:    "file\x00name.txt",
			expected: "filename.txt",
		},
		{
			name:     "replaces special chars",
			input:    "my file (1).txt",
			expected: "my_file__1_.txt",
		},
		{
			name:     "preserves hyphens and underscores",
			input:    "my-file_v2.tar.gz",
			expected: "my-file_v2.tar.gz",
		},
		{
			name:     "replaces unicode",
			input:    "café.txt",
			expected: "caf_.txt",
		},
		{
			name:     "long filename truncated to 255",
			input:    strings.Repeat("a", 300) + ".txt",
			expected: strings.Repeat("a", 255),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeFilename(tt.input)
			if got != tt.expected {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSanitizeFilename_LengthLimit(t *testing.T) {
	name := strings.Repeat("x", 300)
	result := SanitizeFilename(name)
	if len(result) > 255 {
		t.Errorf("expected max 255 chars, got %d", len(result))
	}
}

func TestSanitizeFilename_EmptyAfterBase(t *testing.T) {
	// filepath.Base of empty string returns "."
	result := SanitizeFilename("")
	if result != "." {
		t.Errorf("expected '.', got %q", result)
	}
}

func TestSanitizeFilename_DotDot(t *testing.T) {
	result := SanitizeFilename("../../etc/passwd")
	if strings.Contains(result, "..") {
		t.Errorf("expected no '..', got %q", result)
	}
}
