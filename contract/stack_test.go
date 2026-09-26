package contract

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
		{"velocity/problem.TestExtractFunctionName", "TestExtractFunctionName"},
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
		{"velocity/contract.Function", "velocity/contract"},
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
		Frames: []StackFrame{
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

func TestStackFrame_ShortFile(t *testing.T) {
	tests := []struct {
		name string
		file string
		want string
	}{
		{"pkg path", "/home/user/project/pkg/problem/handler.go", "pkg/problem/handler.go"},
		{"internal path", "/home/user/project/internal/app/main.go", "internal/app/main.go"},
		{"cmd path", "/home/user/project/cmd/server/main.go", "cmd/server/main.go"},
		{"src path", "/home/user/project/src/app/main.go", "src/app/main.go"},
		{"no marker", "/home/user/file.go", "file.go"},
		{"just filename", "main.go", "main.go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := StackFrame{File: tt.file}
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

func TestCaptureStackTrace_Empty(t *testing.T) {
	// Skip a very large number of frames to get empty result
	st := CaptureStackTrace(1000)
	if st == nil {
		t.Fatal("Should return non-nil StackTrace")
	}
	// May have empty frames depending on call depth
}

func TestCaptureStackTrace_WithRuntimeFrames(t *testing.T) {
	// Capture with skip 0 should include this function
	st := CaptureStackTrace(0)

	if st == nil {
		t.Fatal("StackTrace should not be nil")
	}
	if len(st.Frames) == 0 {
		t.Fatal("Should have at least one frame")
	}

	// First frame should be this test function
	found := false
	for _, frame := range st.Frames {
		if frame.Function == "TestCaptureStackTrace_WithRuntimeFrames" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Should find test function in stack")
	}
}

// TestSplitSymbol asserts a function symbol splits into its import path
// and the function's full name within the package at the first dot after
// the last slash, so pkg + "." + fn is the symbol for every symbol with a
// dot, and one without a dot is all function.
func TestSplitSymbol(t *testing.T) {
	tests := []struct {
		name, sym, wantPkg, wantFn string
	}{
		{"plain function", "main.main", "main", "main"},
		{"init function", "main.init.0", "main", "init.0"},
		{"method", "example.com/shop/cart.Cart.Total", "example.com/shop/cart", "Cart.Total"},
		{"pointer method", "github.com/velocitykode/velocity/router.(*VelocityRouterV2).ServeHTTP", "github.com/velocitykode/velocity/router", "(*VelocityRouterV2).ServeHTTP"},
		{"nested closure", "main.run.func2.1.1", "main", "run.func2.1.1"},
		{"closure in a method", "example.com/p.(*T).Run.func1", "example.com/p", "(*T).Run.func1"},
		{"method value", "example.com/p.T.M-fm", "example.com/p", "T.M-fm"},
		{"generic function", "example.com/p.Map[...]", "example.com/p", "Map[...]"},
		{"closure in a generic function", "example.com/p.Map[...].func1", "example.com/p", "Map[...].func1"},
		{"generic method", "example.com/p.(*Box[...]).Get", "example.com/p", "(*Box[...]).Get"},
		{"type arguments naming another path", "example.com/p.Map[example.com/q.T,go.shape.int]", "example.com/p", "Map[example.com/q.T,go.shape.int]"},
		{"%2e-encoded path element", "gopkg.in/yaml%2ev3.Unmarshal", "gopkg.in/yaml%2ev3", "Unmarshal"},
		{"dotted earlier path element", "github.com/foo.bar/pkg.Func.func1", "github.com/foo.bar/pkg", "Func.func1"},
		{"no dot", "simple", "", "simple"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pkg, fn := splitSymbol(tt.sym)
			if pkg != tt.wantPkg || fn != tt.wantFn {
				t.Fatalf("splitSymbol(%q) = (%q, %q), want (%q, %q)", tt.sym, pkg, fn, tt.wantPkg, tt.wantFn)
			}
			if got := extractPackageName(tt.sym); got != pkg {
				t.Errorf("extractPackageName(%q) = %q, want %q", tt.sym, got, pkg)
			}
			if got := extractFunctionName(tt.sym); got != fn {
				t.Errorf("extractFunctionName(%q) = %q, want %q", tt.sym, got, fn)
			}
			if pkg != "" && pkg+"."+fn != tt.sym {
				t.Errorf("%q + \".\" + %q = %q, want the symbol %q", pkg, fn, pkg+"."+fn, tt.sym)
			}
		})
	}
}

// rawStack returns the caller's stack as CaptureStackTrace sees it
// (runtime frames dropped), as runtime symbols with file and line.
func rawStack() []runtime.Frame {
	var pcs [32]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	var out []runtime.Frame
	for {
		f, more := frames.Next()
		if !strings.HasPrefix(f.Function, "runtime.") {
			out = append(out, f)
		}
		if !more {
			return out
		}
	}
}

// TestCaptureStackTrace_ClosureNamedFully asserts a frame captured inside
// a nested closure names the closure fully (Package the import path,
// Function the full name within it), and that String prints every frame
// as its runtime symbol, as before the split moved.
func TestCaptureStackTrace_ClosureNamedFully(t *testing.T) {
	var (
		st  *StackTrace
		raw []runtime.Frame
	)
	func() {
		func() {
			st, raw = CaptureStackTrace(0), rawStack()
		}()
	}()

	const pkgPath = "github.com/velocitykode/velocity/contract"
	first := st.Frames[0]
	if first.Package != pkgPath {
		t.Errorf("Package = %q, want %q", first.Package, pkgPath)
	}
	// The compiler names the inner closure "<test>.func1.1", or with the
	// outer closure inlined "<test>.<test>.func1.func2"; either way the
	// whole chain from the test function down is kept.
	if prefix := "TestCaptureStackTrace_ClosureNamedFully."; !strings.HasPrefix(first.Function, prefix) || !strings.Contains(first.Function[len(prefix):], "func") {
		t.Errorf("Function = %q, want the closure's full name under %q", first.Function, prefix)
	}

	if len(raw) != len(st.Frames) {
		t.Fatalf("captured %d frames, runtime reports %d", len(st.Frames), len(raw))
	}
	var want strings.Builder
	for i, f := range raw {
		fmt.Fprintf(&want, "#%d %s:%d\n    %s\n", i, f.File, f.Line, f.Function)
		if got := st.Frames[i].Package + "." + st.Frames[i].Function; got != f.Function {
			t.Errorf("frame %d: Package.Function = %q, want the symbol %q", i, got, f.Function)
		}
	}
	if got := st.String(); got != want.String() {
		t.Errorf("String() =\n%s\nwant\n%s", got, want.String())
	}
}
