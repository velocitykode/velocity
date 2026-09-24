package exceptions

import (
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
