package exceptions

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"reflect"
	"strings"

	"github.com/velocitykode/velocity/contract"
)

// Renderer is the interface for exception renderers.
type Renderer = contract.Renderer

// Conformance assertions for the concrete renderers.
var (
	_ contract.Renderer = (*JSONRenderer)(nil)
	_ contract.Renderer = (*HTMLRenderer)(nil)
)

// setJSONHeaders sets the headers shared by every JSON exception writer.
func setJSONHeaders(ctx RenderContext) {
	ctx.SetHeader("Content-Type", "application/json")
	ctx.SetHeader("X-Content-Type-Options", "nosniff")
}

// statusCoder is an error that names its HTTP status.
type statusCoder interface {
	GetStatusCode() int
}

// statusMessageError names its HTTP status and a client-facing message.
type statusMessageError interface {
	GetStatusCode() int
	GetMessage() string
}

// httpStatusError names its HTTP status and response headers. *HttpException
// satisfies it, and so does every type embedding *HttpException (for example
// TooManyRequestsException) through the promoted methods. Status and headers
// resolve against this interface rather than *HttpException because
// errors.As never reaches an embedded *HttpException: BaseException.Unwrap
// returns the previous error, not the embedding parent.
type httpStatusError interface {
	GetStatusCode() int
	GetHeaders() map[string]string
}

// resolveHTTPStatus returns the status and response headers for err. The
// first error in err's chain (errors.As) carrying both status and headers
// wins; failing that, the first carrying only a status names it with no
// headers. Everything else is a 500.
func resolveHTTPStatus(err error) (int, map[string]string) {
	var withHeaders httpStatusError
	if errors.As(err, &withHeaders) {
		return withHeaders.GetStatusCode(), withHeaders.GetHeaders()
	}
	var status statusCoder
	if errors.As(err, &status) {
		return status.GetStatusCode(), nil
	}
	return http.StatusInternalServerError, nil
}

// setExceptionHeaders copies an exception's response headers onto ctx,
// dropping any header whose name or value contains CR or LF.
func setExceptionHeaders(ctx RenderContext, headers map[string]string) {
	for k, v := range headers {
		if strings.ContainsAny(k, "\r\n") || strings.ContainsAny(v, "\r\n") {
			continue
		}
		ctx.SetHeader(k, v)
	}
}

// JSONRenderer renders exceptions as JSON.
type JSONRenderer struct{}

// NewJSONRenderer creates a new JSONRenderer.
func NewJSONRenderer() *JSONRenderer {
	return &JSONRenderer{}
}

// ContentType returns the JSON content type.
func (r *JSONRenderer) ContentType() string {
	return "application/json"
}

// Render renders the exception as JSON.
func (r *JSONRenderer) Render(ctx RenderContext, err error, exCtx *ErrorContext, debug bool) error {
	response := make(map[string]any)

	statusCode, headers := resolveHTTPStatus(err)
	setExceptionHeaders(ctx, headers)

	// Build response
	response["message"] = getErrorMessage(err, debug)

	if exCtx != nil {
		if exCtx.RequestID != "" {
			response["request_id"] = exCtx.RequestID
		}
		if exCtx.TraceID != "" {
			response["trace_id"] = exCtx.TraceID
		}
	}

	// Add debug information
	if debug {
		response["exception"] = getExceptionType(err)

		var exc Exception
		if errors.As(err, &exc) {
			if ctx := exc.GetContext(); len(ctx) > 0 {
				response["context"] = ctx
			}
			if prev := exc.GetPrevious(); prev != nil {
				response["previous"] = prev.Error()
			}
		}

		if exCtx != nil && exCtx.StackTrace != nil {
			var frames []map[string]any
			for _, frame := range exCtx.StackTrace.Frames {
				frames = append(frames, map[string]any{
					"file":     frame.File,
					"line":     frame.Line,
					"function": frame.Function,
					"package":  frame.Package,
				})
			}
			response["stack_trace"] = frames
		}
	}

	setJSONHeaders(ctx)
	ctx.WriteHeader(statusCode)

	data, jsonErr := json.Marshal(response)
	if jsonErr != nil {
		return jsonErr
	}

	_, writeErr := ctx.Write(data)
	return writeErr
}

// HTMLRenderer renders exceptions as HTML.
type HTMLRenderer struct {
	debugTemplate *template.Template
	errorTemplate *template.Template
}

// NewHTMLRenderer creates a new HTMLRenderer with the embedded templates.
func NewHTMLRenderer() *HTMLRenderer {
	r := &HTMLRenderer{}
	r.debugTemplate = template.Must(template.New("debug").Funcs(templateFuncs).Parse(debugTemplateHTML))
	r.errorTemplate = template.Must(template.New("error").Funcs(templateFuncs).Parse(errorTemplateHTML))
	return r
}

// NewHTMLRendererWithTemplates creates a new HTMLRenderer with custom templates.
func NewHTMLRendererWithTemplates(debugTpl, errorTpl *template.Template) *HTMLRenderer {
	return &HTMLRenderer{
		debugTemplate: debugTpl,
		errorTemplate: errorTpl,
	}
}

// ContentType returns the HTML content type.
func (r *HTMLRenderer) ContentType() string {
	return "text/html"
}

// Render renders the exception as HTML.
func (r *HTMLRenderer) Render(ctx RenderContext, err error, exCtx *ErrorContext, debug bool) error {
	statusCode, headers := resolveHTTPStatus(err)
	setExceptionHeaders(ctx, headers)

	data := &templateData{
		StatusCode:    statusCode,
		StatusText:    http.StatusText(statusCode),
		Message:       getErrorMessage(err, debug),
		ExceptionType: getExceptionType(err),
		Debug:         debug,
	}

	if exCtx != nil {
		data.RequestID = exCtx.RequestID
		data.TraceID = exCtx.TraceID
		data.Method = exCtx.Method
		data.URL = exCtx.URL
		data.Timestamp = exCtx.Timestamp.Format("2006-01-02 15:04:05")

		if debug && exCtx.StackTrace != nil {
			data.Frames = exCtx.StackTrace.GetFramesWithSource(5)
		}
	}

	var exc Exception
	if debug && errors.As(err, &exc) {
		data.Context = exc.GetContext()
		if prev := exc.GetPrevious(); prev != nil {
			data.Previous = prev.Error()
		}
	}

	ctx.SetHeader("Content-Type", "text/html; charset=utf-8")
	ctx.WriteHeader(statusCode)

	tpl := r.errorTemplate
	if debug {
		tpl = r.debugTemplate
	}

	return tpl.Execute(&responseWriter{ctx}, data)
}

// templateData holds data for HTML templates.
type templateData struct {
	StatusCode    int
	StatusText    string
	Message       string
	ExceptionType string
	RequestID     string
	TraceID       string
	Method        string
	URL           string
	Timestamp     string
	Debug         bool
	Frames        []FrameWithSource
	Context       map[string]any
	Previous      string
}

// responseWriter adapts RenderContext to io.Writer.
type responseWriter struct {
	ctx RenderContext
}

func (w *responseWriter) Write(p []byte) (int, error) {
	return w.ctx.Write(p)
}

// getErrorMessage returns the appropriate error message based on debug mode.
// Outside debug mode a status-carrying error in err's chain (errors.As)
// decides: 5xx shows only the status text, 4xx shows that error's own
// message (never the wrapper text around it) when it has one. Anything else
// shows a generic message.
func getErrorMessage(err error, debug bool) string {
	if debug {
		return err.Error()
	}

	var withMessage statusMessageError
	if errors.As(err, &withMessage) {
		if code := withMessage.GetStatusCode(); code >= 500 {
			return http.StatusText(code)
		}
		return withMessage.GetMessage()
	}

	var status statusCoder
	if errors.As(err, &status) {
		if code := status.GetStatusCode(); code >= 500 {
			return http.StatusText(code)
		}
	}

	// For non-HTTP exceptions in production, show generic message
	return "An error occurred"
}

// getExceptionType returns a type name for err. The framework's own builtin
// exception types render under their bare names (e.g. "ValidationException")
// with the leading pointer marker and "exceptions." qualifier stripped. Every
// other error keeps its full %T name, pointer marker and all (e.g.
// "*mypkg.MyError", "*errors.errorString" for errors.New), so it stays
// matchable via fmt.Sprintf("%T", err) in WithDontReport. Returns "" for a nil
// error.
func getExceptionType(err error) string {
	if err == nil {
		return ""
	}
	raw := fmt.Sprintf("%T", err)
	// Only the framework's own exceptions package gets its decoration stripped;
	// a third-party type from a package also named "exceptions" keeps its full
	// %T name so it stays matchable by that name.
	t := reflect.TypeOf(err)
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t != nil && t.PkgPath() == "github.com/velocitykode/velocity/exceptions" {
		return strings.TrimPrefix(strings.TrimLeft(raw, "*&"), "exceptions.")
	}
	return raw
}

// NegotiateRenderer selects the appropriate renderer based on content negotiation.
func NegotiateRenderer(ctx RenderContext, renderers map[string]Renderer) Renderer {
	accept := requestHeader(ctx, "Accept")

	// Check for JSON preference in Accept header
	if strings.Contains(accept, "application/json") {
		if r, ok := renderers["json"]; ok {
			return r
		}
	}

	// Check for HTML preference in Accept header
	if strings.Contains(accept, "text/html") {
		if r, ok := renderers["html"]; ok {
			return r
		}
	}

	// Check WantsJSON for API paths, AJAX, Content-Type, etc.
	if ctx.WantsJSON() {
		if r, ok := renderers["json"]; ok {
			return r
		}
	}

	// Default to HTML for browser requests with empty or wildcard Accept
	if accept == "" || accept == "*/*" {
		if r, ok := renderers["html"]; ok {
			return r
		}
	}

	// Fall back to HTML
	if r, ok := renderers["html"]; ok {
		return r
	}

	// Last resort: JSON
	if r, ok := renderers["json"]; ok {
		return r
	}

	return NewJSONRenderer()
}

// template helper functions
var templateFuncs = template.FuncMap{
	"add": func(a, b int) int {
		return a + b
	},
	"sub": func(a, b int) int {
		return a - b
	},
}
