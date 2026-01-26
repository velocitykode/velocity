package exceptions

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
)

// Renderer is the interface for exception renderers.
type Renderer interface {
	Render(ctx RenderContext, err error, exCtx *ExceptionContext, debug bool) error
	ContentType() string
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
func (r *JSONRenderer) Render(ctx RenderContext, err error, exCtx *ExceptionContext, debug bool) error {
	statusCode := http.StatusInternalServerError
	response := make(map[string]any)

	// Handle HTTP exceptions
	if httpExc, ok := err.(*HttpException); ok {
		statusCode = httpExc.StatusCode
		for k, v := range httpExc.GetHeaders() {
			ctx.SetHeader(k, v)
		}
	} else if exc, ok := err.(interface{ GetStatusCode() int }); ok {
		statusCode = exc.GetStatusCode()
	}

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

		if exc, ok := err.(Exception); ok {
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

	ctx.SetHeader("Content-Type", "application/json")
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
func (r *HTMLRenderer) Render(ctx RenderContext, err error, exCtx *ExceptionContext, debug bool) error {
	statusCode := http.StatusInternalServerError

	// Handle HTTP exceptions
	if httpExc, ok := err.(*HttpException); ok {
		statusCode = httpExc.StatusCode
		for k, v := range httpExc.GetHeaders() {
			ctx.SetHeader(k, v)
		}
	} else if exc, ok := err.(interface{ GetStatusCode() int }); ok {
		statusCode = exc.GetStatusCode()
	}

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

	if exc, ok := err.(Exception); ok && debug {
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
func getErrorMessage(err error, debug bool) string {
	if debug {
		return err.Error()
	}

	// In production, show generic messages for server errors
	if httpExc, ok := err.(*HttpException); ok {
		if httpExc.StatusCode >= 500 {
			return http.StatusText(httpExc.StatusCode)
		}
		return httpExc.GetMessage()
	}

	// Check for types that embed HttpException and implement GetStatusCode
	if exc, ok := err.(interface{ GetStatusCode() int }); ok {
		code := exc.GetStatusCode()
		if code >= 500 {
			return http.StatusText(code)
		}
		// For client errors (4xx), return the message
		if msgProvider, ok := err.(interface{ GetMessage() string }); ok {
			return msgProvider.GetMessage()
		}
	}

	// For non-HTTP exceptions in production, show generic message
	return "An error occurred"
}

// getExceptionType returns the type name of the exception.
func getExceptionType(err error) string {
	t := strings.TrimPrefix(strings.TrimPrefix(
		strings.Replace(
			strings.Replace(
				getTypeName(err),
				"*", "", -1),
			"exceptions.", "", -1),
		"velocity/pkg/"), "")
	return t
}

// getTypeName returns the full type name of an error.
func getTypeName(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimPrefix(
		strings.Replace(
			getTypeString(err),
			"*", "", -1),
		"&")
}

// getTypeString returns the type as a string.
func getTypeString(v any) string {
	if v == nil {
		return ""
	}
	return strings.Replace(
		strings.Replace(
			strings.TrimPrefix(
				strings.TrimPrefix(
					getFullTypeName(v),
					"*"),
				"&"),
			"(", "", -1),
		")", "", -1)
}

// getFullTypeName returns the full type name using reflection-free method.
func getFullTypeName(v any) string {
	if v == nil {
		return ""
	}
	// Use %T format verb to get type name
	return strings.TrimPrefix(
		strings.TrimPrefix(
			formatType(v),
			"*"),
		"&")
}

// formatType formats the type of a value.
func formatType(v any) string {
	return strings.Replace(
		strings.Replace(
			getType(v),
			"(", "", -1),
		")", "", -1)
}

// getType uses fmt to get the type name.
func getType(v any) string {
	if v == nil {
		return ""
	}
	// Simple type extraction using string formatting
	s := ""
	switch v.(type) {
	case *HttpException:
		s = "HttpException"
	case *NotFoundHttpException:
		s = "NotFoundHttpException"
	case *UnauthorizedHttpException:
		s = "UnauthorizedHttpException"
	case *ForbiddenHttpException:
		s = "ForbiddenHttpException"
	case *ValidationException:
		s = "ValidationException"
	case *TooManyRequestsException:
		s = "TooManyRequestsException"
	case *ServiceUnavailableException:
		s = "ServiceUnavailableException"
	case *MethodNotAllowedHttpException:
		s = "MethodNotAllowedHttpException"
	case *ConflictHttpException:
		s = "ConflictHttpException"
	case *GoneHttpException:
		s = "GoneHttpException"
	case *BadRequestHttpException:
		s = "BadRequestHttpException"
	case *InternalServerErrorException:
		s = "InternalServerErrorException"
	case *BaseException:
		s = "BaseException"
	default:
		s = "error"
	}
	return s
}

// NegotiateRenderer selects the appropriate renderer based on content negotiation.
func NegotiateRenderer(ctx RenderContext, renderers map[string]Renderer) Renderer {
	accept := ctx.GetHeader("Accept")

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
