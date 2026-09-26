package contract

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// StackFrame represents a single stack frame.
type StackFrame struct {
	File     string
	Line     int
	Function string
	Package  string
}

// ShortFile returns a shortened version of the file path.
func (f *StackFrame) ShortFile() string {
	// Try to find a common base like "pkg/" or "internal/"
	for _, marker := range []string{"/pkg/", "/internal/", "/cmd/", "/src/"} {
		if idx := strings.Index(f.File, marker); idx >= 0 {
			return f.File[idx+1:]
		}
	}

	// Fall back to just the filename
	return filepath.Base(f.File)
}

// StackTrace represents a captured stack trace.
type StackTrace struct {
	Frames []StackFrame
}

// CaptureStackTrace captures the current goroutine's stack, skipping skip
// frames above its caller (skip 0 starts at the function calling
// CaptureStackTrace). Runtime frames are omitted; at most 32 frames are
// kept.
func CaptureStackTrace(skip int) *StackTrace {
	const maxDepth = 32
	var pcs [maxDepth]uintptr

	// Skip additional frames for runtime.Callers and CaptureStackTrace itself
	n := runtime.Callers(skip+2, pcs[:])
	if n == 0 {
		return &StackTrace{Frames: []StackFrame{}}
	}

	frames := runtime.CallersFrames(pcs[:n])
	trace := &StackTrace{Frames: make([]StackFrame, 0, n)}

	for {
		frame, more := frames.Next()

		// Skip runtime frames
		if strings.HasPrefix(frame.Function, "runtime.") {
			if !more {
				break
			}
			continue
		}

		pkg, fn := splitSymbol(frame.Function)
		trace.Frames = append(trace.Frames, StackFrame{
			File:     frame.File,
			Line:     frame.Line,
			Function: fn,
			Package:  pkg,
		})

		if !more {
			break
		}
	}

	return trace
}

// extractFunctionName returns the function's name within its package from
// a function symbol as runtime.Frame.Function names it, receiver and
// closure suffixes included: "run.func2.1" for "main.run.func2.1",
// "(*T).M" for "example.com/p.(*T).M". See splitSymbol.
func extractFunctionName(fullName string) string {
	_, fn := splitSymbol(fullName)
	return fn
}

// extractPackageName returns the import path of a function symbol as
// runtime.Frame.Function names it: "main" for "main.run.func2.1",
// "example.com/p" for "example.com/p.(*T).M". See splitSymbol.
func extractPackageName(fullName string) string {
	pkg, _ := splitSymbol(fullName)
	return pkg
}

// splitSymbol splits a function symbol into its import path and the
// function's name within the package at the first dot after the path's
// last slash. The Go runtime writes a dot inside the last element of an
// import path as %2e ("gopkg.in/yaml%2ev3.Unmarshal"), so that dot always
// ends the path, and everything after it (a method's receiver, a closure's
// func2.1 suffixes, a generic's type arguments) is the function's. The
// last slash is looked for before the first '[' only, so a slash inside
// type arguments cannot move the boundary. For a symbol with a dot,
// pkg + "." + fn is the symbol; a symbol without one is all function and
// no package.
func splitSymbol(sym string) (pkg, fn string) {
	head := sym
	if i := strings.IndexByte(head, '['); i >= 0 {
		head = head[:i]
	}
	start := strings.LastIndexByte(head, '/') + 1
	if dot := strings.IndexByte(sym[start:], '.'); dot >= 0 {
		return sym[:start+dot], sym[start+dot+1:]
	}
	return "", sym
}

// String returns a formatted string representation of the stack trace.
func (st *StackTrace) String() string {
	var sb strings.Builder
	for i, frame := range st.Frames {
		sb.WriteString(fmt.Sprintf("#%d %s:%d\n", i, frame.File, frame.Line))
		sb.WriteString(fmt.Sprintf("    %s.%s\n", frame.Package, frame.Function))
	}
	return sb.String()
}

// GetFramesWithSource returns stack frames with source code context.
func (st *StackTrace) GetFramesWithSource(contextLines int) []FrameWithSource {
	result := make([]FrameWithSource, len(st.Frames))

	for i, frame := range st.Frames {
		fws := FrameWithSource{StackFrame: frame}

		// Only try to get source for files that exist
		if frame.File != "" && fileExists(frame.File) {
			source, err := GetSourceContext(frame.File, frame.Line, contextLines)
			if err != nil {
				fws.SourceErr = err
			} else {
				fws.Source = source
			}
		}

		result[i] = fws
	}

	return result
}

// SourceLine represents a line of source code.
type SourceLine struct {
	Number    int
	Content   string
	Highlight bool
}

// FrameWithSource represents a stack frame with its source context.
type FrameWithSource struct {
	StackFrame
	Source    []SourceLine
	SourceErr error
}

// GetSourceContext retrieves source code lines around a specific line in a file.
// It returns contextLines lines before and after the target line.
func GetSourceContext(file string, line int, contextLines int) ([]SourceLine, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	startLine := line - contextLines
	if startLine < 1 {
		startLine = 1
	}
	endLine := line + contextLines

	var lines []SourceLine
	scanner := bufio.NewScanner(f)
	currentLine := 0

	for scanner.Scan() {
		currentLine++
		if currentLine < startLine {
			continue
		}
		if currentLine > endLine {
			break
		}

		lines = append(lines, SourceLine{
			Number:    currentLine,
			Content:   scanner.Text(),
			Highlight: currentLine == line,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return lines, nil
}

// fileExists checks if a file exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
