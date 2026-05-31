package exceptions

import (
	"os"
	"runtime"
	"strings"

	"github.com/velocitykode/velocity/contract"
)

// Frame represents a single stack frame.
type Frame = contract.StackFrame

// StackTrace represents a captured stack trace.
type StackTrace = contract.StackTrace

// SourceLine represents a line of source code.
type SourceLine = contract.SourceLine

// FrameWithSource represents a stack frame with its source context.
type FrameWithSource = contract.FrameWithSource

// GetSourceContext retrieves source code lines around a specific line in a file.
// It returns contextLines lines before and after the target line.
var GetSourceContext = contract.GetSourceContext

// CaptureStackTrace captures the current stack trace, skipping the specified number of frames.
func CaptureStackTrace(skip int) *StackTrace {
	const maxDepth = 32
	var pcs [maxDepth]uintptr

	// Skip additional frames for runtime.Callers and CaptureStackTrace itself
	n := runtime.Callers(skip+2, pcs[:])
	if n == 0 {
		return &StackTrace{Frames: []Frame{}}
	}

	frames := runtime.CallersFrames(pcs[:n])
	trace := &StackTrace{Frames: make([]Frame, 0, n)}

	for {
		frame, more := frames.Next()

		// Skip runtime frames
		if strings.HasPrefix(frame.Function, "runtime.") {
			if !more {
				break
			}
			continue
		}

		f := Frame{
			File:     frame.File,
			Line:     frame.Line,
			Function: extractFunctionName(frame.Function),
			Package:  extractPackageName(frame.Function),
		}

		trace.Frames = append(trace.Frames, f)

		if !more {
			break
		}
	}

	return trace
}

// extractFunctionName extracts the function name from a fully qualified name.
func extractFunctionName(fullName string) string {
	// Split by last slash to get package.function
	lastSlash := strings.LastIndex(fullName, "/")
	if lastSlash >= 0 {
		fullName = fullName[lastSlash+1:]
	}

	// Find the last dot that separates package from function
	lastDot := strings.LastIndex(fullName, ".")
	if lastDot >= 0 {
		return fullName[lastDot+1:]
	}

	return fullName
}

// extractPackageName extracts the package name from a fully qualified function name.
func extractPackageName(fullName string) string {
	// Find the last dot that separates package from function
	lastDot := strings.LastIndex(fullName, ".")
	if lastDot >= 0 {
		return fullName[:lastDot]
	}
	return ""
}

// fileExists checks if a file exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
