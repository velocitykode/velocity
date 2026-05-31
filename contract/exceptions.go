package contract

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RenderContext provides the context needed for rendering exceptions.
type RenderContext interface {
	WriteHeader(statusCode int)
	Write(data []byte) (int, error)
	SetHeader(key, value string)
	GetHeader(key string) string
	RequestPath() string
	RequestMethod() string
	WantsJSON() bool
}

// ExceptionHandler is the interface satisfied by the exceptions package
// Handler. It covers the methods used through app.Services and
// router.Context.
type ExceptionHandler interface {
	// Handle reports and renders an exception in one call.
	Handle(ctx RenderContext, err error)
	// HandleWithContext reports and renders with a provided exception context.
	HandleWithContext(ctx RenderContext, err error, exCtx *ExceptionContext)
	// HandlePanic handles a recovered panic value.
	HandlePanic(ctx RenderContext, recovered any)
	// Report reports an exception to all configured reporters.
	Report(err error, ctx *ExceptionContext)
	// Render renders an exception response.
	Render(ctx RenderContext, err error, exCtx *ExceptionContext)
	// ShouldReport determines if an exception should be reported.
	ShouldReport(err error) bool

	// Configuration methods used during bootstrap.
	SetDebug(debug bool)
	IsDebug() bool
	SetEnvironment(env string)
	GetEnvironment() string
	AddReporter(reporter Reporter)
	SetReporters(reporters ...Reporter)
	AddRenderer(contentType string, renderer Renderer)
	DontReport(exceptionType string)
	SetAPIMode(enabled bool)
	IsAPIMode() bool
	SetAPIPrefixes(prefixes ...string)
	GetAPIPrefixes() []string
	RegisterCustomHandler(exceptionType any, handler func(RenderContext, error, *ExceptionContext))
}

// Reporter is the interface for exception reporters.
type Reporter interface {
	Report(err error, ctx *ExceptionContext)
}

// Renderer is the interface for exception renderers.
type Renderer interface {
	Render(ctx RenderContext, err error, exCtx *ExceptionContext, debug bool) error
	ContentType() string
}

// ExceptionContext contains contextual information about where an exception occurred.
type ExceptionContext struct {
	RequestID  string
	TraceID    string
	UserID     string
	URL        string
	Method     string
	IP         string
	UserAgent  string
	Timestamp  time.Time
	StackTrace *StackTrace
	Extra      map[string]any
}

// WithRequestInfo adds request information to the context.
func (c *ExceptionContext) WithRequestInfo(method, url, ip, userAgent string) *ExceptionContext {
	c.Method = method
	c.URL = url
	c.IP = ip
	c.UserAgent = userAgent
	return c
}

// WithIDs adds request and trace IDs to the context.
func (c *ExceptionContext) WithIDs(requestID, traceID string) *ExceptionContext {
	c.RequestID = requestID
	c.TraceID = traceID
	return c
}

// WithUserID adds user ID to the context.
func (c *ExceptionContext) WithUserID(userID string) *ExceptionContext {
	c.UserID = userID
	return c
}

// WithStackTrace adds a stack trace to the context.
func (c *ExceptionContext) WithStackTrace(st *StackTrace) *ExceptionContext {
	c.StackTrace = st
	return c
}

// WithExtra adds extra data to the context.
func (c *ExceptionContext) WithExtra(key string, value any) *ExceptionContext {
	if c.Extra == nil {
		c.Extra = make(map[string]any)
	}
	c.Extra[key] = value
	return c
}

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
