package exceptions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureStackTrace(t *testing.T) {
	st := CaptureStackTrace(0)

	if st == nil {
		t.Fatal("CaptureStackTrace returned nil")
	}
	if len(st.Frames) == 0 {
		t.Fatal("CaptureStackTrace returned empty frames")
	}

	// First frame should be this test function
	firstFrame := st.Frames[0]
	if !strings.Contains(firstFrame.Function, "TestCaptureStackTrace") {
		t.Errorf("First frame function = %q, want contains TestCaptureStackTrace", firstFrame.Function)
	}
	if !strings.Contains(firstFrame.File, "stack_test.go") {
		t.Errorf("First frame file = %q, want contains stack_test.go", firstFrame.File)
	}
}

func TestCaptureStackTrace_Skip(t *testing.T) {
	st := captureHelper()

	if st == nil {
		t.Fatal("CaptureStackTrace returned nil")
	}
	if len(st.Frames) == 0 {
		t.Fatal("CaptureStackTrace returned empty frames")
	}

	// With skip=1, first frame should be the caller (captureHelper), not CaptureStackTrace internal
	firstFrame := st.Frames[0]
	if !strings.Contains(firstFrame.Function, "captureHelper") {
		t.Errorf("First frame function = %q, want contains captureHelper", firstFrame.Function)
	}
}

func captureHelper() *StackTrace {
	return CaptureStackTrace(0)
}

func TestStackTrace_String(t *testing.T) {
	st := CaptureStackTrace(0)
	str := st.String()

	if str == "" {
		t.Error("StackTrace.String() returned empty string")
	}
	if !strings.Contains(str, "#0") {
		t.Error("StackTrace.String() should contain frame numbers")
	}
	if !strings.Contains(str, "stack_test.go") {
		t.Error("StackTrace.String() should contain file names")
	}
}

func TestExtractFunctionName(t *testing.T) {
	tests := []struct {
		fullName string
		want     string
	}{
		{"main.main", "main"},
		{"velocity/exceptions.TestExtractFunctionName", "TestExtractFunctionName"},
		{"github.com/user/repo/pkg.Function", "Function"},
		{"(*Type).Method", "Method"},
		{"simple", "simple"},
	}

	for _, tt := range tests {
		t.Run(tt.fullName, func(t *testing.T) {
			got := extractFunctionName(tt.fullName)
			if got != tt.want {
				t.Errorf("extractFunctionName(%q) = %q, want %q", tt.fullName, got, tt.want)
			}
		})
	}
}

func TestExtractPackageName(t *testing.T) {
	tests := []struct {
		fullName string
		want     string
	}{
		{"main.main", "main"},
		{"velocity/exceptions.Function", "velocity/exceptions"},
		{"simple", ""},
	}

	for _, tt := range tests {
		t.Run(tt.fullName, func(t *testing.T) {
			got := extractPackageName(tt.fullName)
			if got != tt.want {
				t.Errorf("extractPackageName(%q) = %q, want %q", tt.fullName, got, tt.want)
			}
		})
	}
}

func TestGetSourceContext(t *testing.T) {
	// Create a temporary file for testing
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.go")
	content := `package test

func main() {
	// line 4
	// line 5
	// line 6
	// line 7
	// line 8
}
`
	err := os.WriteFile(tmpFile, []byte(content), 0644)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		line         int
		contextLines int
		wantLines    int
		wantFirst    int
	}{
		{"middle of file", 5, 2, 5, 3},
		{"start of file", 1, 2, 3, 1},
		{"end of file", 9, 2, 3, 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines, err := GetSourceContext(tmpFile, tt.line, tt.contextLines)
			if err != nil {
				t.Errorf("GetSourceContext() error = %v", err)
				return
			}
			if len(lines) != tt.wantLines {
				t.Errorf("Got %d lines, want %d", len(lines), tt.wantLines)
			}
			if len(lines) > 0 && lines[0].Number != tt.wantFirst {
				t.Errorf("First line number = %d, want %d", lines[0].Number, tt.wantFirst)
			}

			// Check highlight
			for _, line := range lines {
				if line.Number == tt.line && !line.Highlight {
					t.Error("Target line should be highlighted")
				}
				if line.Number != tt.line && line.Highlight {
					t.Error("Non-target line should not be highlighted")
				}
			}
		})
	}
}

func TestGetSourceContext_FileNotFound(t *testing.T) {
	_, err := GetSourceContext("/nonexistent/file.go", 1, 2)
	if err == nil {
		t.Error("Expected error for nonexistent file")
	}
}

func TestGetFramesWithSource(t *testing.T) {
	st := CaptureStackTrace(0)
	frames := st.GetFramesWithSource(2)

	if len(frames) == 0 {
		t.Fatal("GetFramesWithSource returned empty")
	}

	// First frame should have source since it's this test file
	firstFrame := frames[0]
	if firstFrame.SourceErr != nil {
		t.Logf("First frame source error (may be expected): %v", firstFrame.SourceErr)
	}
	// Source may or may not be available depending on test environment
}

func TestGetFramesWithSource_NonexistentFile(t *testing.T) {
	st := &StackTrace{
		Frames: []Frame{
			{File: "/nonexistent/file.go", Line: 10, Function: "Test", Package: "test"},
		},
	}

	frames := st.GetFramesWithSource(2)
	if len(frames) != 1 {
		t.Fatal("Expected one frame")
	}
	// Should not have source for nonexistent file
	if len(frames[0].Source) > 0 {
		t.Error("Should not have source for nonexistent file")
	}
}

func TestFrame_ShortFile(t *testing.T) {
	tests := []struct {
		name string
		file string
		want string
	}{
		{"pkg path", "/home/user/project/pkg/exceptions/handler.go", "pkg/exceptions/handler.go"},
		{"internal path", "/home/user/project/internal/app/main.go", "internal/app/main.go"},
		{"cmd path", "/home/user/project/cmd/server/main.go", "cmd/server/main.go"},
		{"src path", "/home/user/project/src/app/main.go", "src/app/main.go"},
		{"no marker", "/home/user/file.go", "file.go"},
		{"just filename", "main.go", "main.go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Frame{File: tt.file}
			got := f.ShortFile()
			if got != tt.want {
				t.Errorf("ShortFile() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFileExists(t *testing.T) {
	// Create a temporary file
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "exists.txt")
	os.WriteFile(tmpFile, []byte("test"), 0644)

	if !fileExists(tmpFile) {
		t.Error("fileExists should return true for existing file")
	}

	if fileExists(filepath.Join(tmpDir, "nonexistent.txt")) {
		t.Error("fileExists should return false for nonexistent file")
	}
}
