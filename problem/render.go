package problem

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"reflect"
	"sync"

	"github.com/velocitykode/velocity/contract"
)

// Renderer renders an error response for one content type.
type Renderer = contract.Renderer

// Conformance assertions for the concrete renderers.
var (
	_ contract.Renderer = (*JSONRenderer)(nil)
	_ contract.Renderer = (*HTMLRenderer)(nil)
)

// ProblemTypeContent is the media type of a problem details body (RFC 9457).
const ProblemTypeContent = "application/problem+json"

// ProblemTyper is an error that names the problem type URI of its problem
// details body. Without it the type is "about:blank".
type ProblemTyper interface {
	error
	ProblemType() string
}

// FieldErrors is an error that carries per-field messages, rendered as the
// "errors" member of a problem details body.
type FieldErrors interface {
	error
	Errors() map[string][]string
}

// problemBody is the problem details document. Field order is the wire
// order.
type problemBody struct {
	Type       string              `json:"type"`
	Title      string              `json:"title"`
	Status     int                 `json:"status"`
	Detail     string              `json:"detail"`
	Instance   string              `json:"instance,omitempty"`
	Errors     map[string][]string `json:"errors,omitempty"`
	RequestID  string              `json:"request_id,omitempty"`
	TraceID    string              `json:"trace_id,omitempty"`
	ErrorType  string              `json:"exception,omitempty"`
	Origin     string              `json:"origin,omitempty"`
	Context    map[string]any      `json:"context,omitempty"`
	Previous   string              `json:"previous,omitempty"`
	StackTrace []stackFrame        `json:"stack_trace,omitempty"`
}

type stackFrame struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function"`
	Package  string `json:"package"`
}

// JSONRenderer renders application/problem+json bodies.
type JSONRenderer struct{}

// NewJSONRenderer returns a JSONRenderer.
func NewJSONRenderer() *JSONRenderer {
	return &JSONRenderer{}
}

// ContentType returns application/problem+json.
func (r *JSONRenderer) ContentType() string {
	return ProblemTypeContent
}

// Render writes the problem details body for err at status. Outside debug,
// detail is the 4xx client message or the status title and no internal
// text is included; debug adds the error type, origin, context, previous
// error and stack trace. The body is encoded before anything is written, so
// an encoding failure leaves the response untouched.
func (r *JSONRenderer) Render(rc RenderContext, err error, ctx *ErrorContext, status int, debug bool) error {
	data, encErr := json.Marshal(buildProblem(rc, err, ctx, status, debug))
	if encErr != nil {
		return encErr
	}
	rc.SetHeader("Content-Type", ProblemTypeContent)
	rc.SetHeader("X-Content-Type-Options", "nosniff")
	rc.WriteHeader(status)
	_, writeErr := rc.Write(data)
	return writeErr
}

// buildProblem assembles the problem details body.
func buildProblem(rc RenderContext, err error, ctx *ErrorContext, status int, debug bool) problemBody {
	body := problemBody{
		Type:   "about:blank",
		Title:  contract.StatusTitle(status),
		Status: status,
		Detail: clientMessage(err, status, debug),
	}
	var typer ProblemTyper
	if errors.As(err, &typer) {
		if t := typer.ProblemType(); t != "" {
			body.Type = t
		}
	}
	if r := requestOf(rc); r != nil && r.URL != nil {
		body.Instance = r.URL.Path
	}
	var fields FieldErrors
	if (debug || status < http.StatusInternalServerError) && errors.As(err, &fields) {
		body.Errors = fields.Errors()
	}
	if ctx != nil {
		body.RequestID = ctx.RequestID
		body.TraceID = ctx.TraceID
	}
	if !debug {
		return body
	}
	body.ErrorType = errorTypeName(err)
	body.Origin = originOf(err)
	body.Context = debugContext(err, ctx)
	if prev := errors.Unwrap(err); prev != nil {
		body.Previous = prev.Error()
	}
	if ctx != nil && ctx.StackTrace != nil {
		for _, f := range ctx.StackTrace.Frames {
			body.StackTrace = append(body.StackTrace, stackFrame{File: f.File, Line: f.Line, Function: f.Function, Package: f.Package})
		}
	}
	return body
}

// errorTypeName returns the Go type name of err, for debug output only.
func errorTypeName(err error) string {
	if err == nil {
		return ""
	}
	return reflect.TypeOf(err).String()
}

// originOf returns the recorded origin of the HTTPError in err's chain.
func originOf(err error) string {
	var he *contract.HTTPError
	if errors.As(err, &he) {
		return he.Origin()
	}
	return ""
}

// debugContext merges ctx.Extra and the Contextual fields of err, with every
// value made JSON-encodable (a value that cannot be encoded is formatted
// with %v). It returns nil when there is nothing to show.
func debugContext(err error, ctx *ErrorContext) map[string]any {
	out := make(map[string]any)
	if ctx != nil {
		for k, v := range ctx.Extra {
			out[k] = v
		}
	}
	var contextual contract.Contextual
	if errors.As(err, &contextual) {
		for k, v := range contextual.Context() {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	for k, v := range out {
		if _, encErr := json.Marshal(v); encErr != nil {
			out[k] = fmt.Sprintf("%v", v)
		}
	}
	return out
}

// PageData is the data an HTML error template receives. The debug-only
// fields (ErrorType, Origin, Frames, Context, Previous) are empty outside
// debug mode.
type PageData struct {
	StatusCode int
	StatusText string
	Message    string
	ErrorType  string
	Origin     string
	RequestID  string
	TraceID    string
	Method     string
	URL        string
	Timestamp  string
	Debug      bool
	Frames     []FrameWithSource
	Context    map[string]any
	Previous   string
}

// HTMLRenderer renders HTML error pages. Outside debug mode it looks up a
// template registered for the exact status, then one for the status class,
// then falls back to the built-in page; debug mode always uses the debug
// page.
type HTMLRenderer struct {
	mu              sync.RWMutex
	debugTemplate   *template.Template
	errorTemplate   *template.Template
	statusTemplates map[int]*template.Template
	classTemplates  map[int]*template.Template
}

// Built-in templates, parsed once. The sources are package constants; a
// parse failure (a build defect the package tests catch) leaves the template
// nil and Render returns the parse error instead of panicking.
var (
	builtinDebugTemplate, errBuiltinDebug = template.New("debug").Funcs(templateFuncs).Parse(debugTemplateHTML)
	builtinErrorTemplate, errBuiltinError = template.New("error").Funcs(templateFuncs).Parse(errorTemplateHTML)
)

// NewHTMLRenderer returns an HTMLRenderer with the built-in templates.
func NewHTMLRenderer() *HTMLRenderer {
	return NewHTMLRendererWithTemplates(nil, nil)
}

// NewHTMLRendererWithTemplates returns an HTMLRenderer with its own debug
// and fallback error templates; a nil template keeps the built-in one.
func NewHTMLRendererWithTemplates(debugTpl, errorTpl *template.Template) *HTMLRenderer {
	if debugTpl == nil {
		debugTpl = builtinDebugTemplate
	}
	if errorTpl == nil {
		errorTpl = builtinErrorTemplate
	}
	return &HTMLRenderer{
		debugTemplate:   debugTpl,
		errorTemplate:   errorTpl,
		statusTemplates: make(map[int]*template.Template),
		classTemplates:  make(map[int]*template.Template),
	}
}

// ErrInvalidTemplate is matched (errors.Is) by the error the template
// registration methods return for a nil template or an out-of-range key.
var ErrInvalidTemplate = errors.New("problem: invalid error template registration")

// RegisterStatusTemplate registers tmpl for responses with exactly status
// (100-999).
func (r *HTMLRenderer) RegisterStatusTemplate(status int, tmpl *template.Template) error {
	if tmpl == nil || status < 100 || status > 999 {
		return fmt.Errorf("%w: status %d", ErrInvalidTemplate, status)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statusTemplates[status] = tmpl
	return nil
}

// RegisterClassTemplate registers tmpl for every status in class 4 (4xx) or
// 5 (5xx) that has no exact status template.
func (r *HTMLRenderer) RegisterClassTemplate(class int, tmpl *template.Template) error {
	if tmpl == nil || (class != 4 && class != 5) {
		return fmt.Errorf("%w: class %d", ErrInvalidTemplate, class)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.classTemplates[class] = tmpl
	return nil
}

// ownsPage reports whether a page registered by the application answers
// status outside debug mode: a template for the exact status or its class,
// or a fallback error template in place of the built-in one.
func (r *HTMLRenderer) ownsPage(status int) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.statusTemplates[status]; ok {
		return true
	}
	if _, ok := r.classTemplates[status/100]; ok {
		return true
	}
	return r.errorTemplate != builtinErrorTemplate
}

// ContentType returns text/html.
func (r *HTMLRenderer) ContentType() string {
	return "text/html"
}

// template picks the template for status.
func (r *HTMLRenderer) template(status int, debug bool) *template.Template {
	if debug {
		return r.debugTemplate
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if t, ok := r.statusTemplates[status]; ok {
		return t
	}
	if t, ok := r.classTemplates[status/100]; ok {
		return t
	}
	return r.errorTemplate
}

// Render writes the HTML page for err at status. The template runs into a
// buffer first, so a template failure leaves the response untouched.
func (r *HTMLRenderer) Render(rc RenderContext, err error, ctx *ErrorContext, status int, debug bool) error {
	data := &PageData{
		StatusCode: status,
		StatusText: contract.StatusTitle(status),
		Message:    clientMessage(err, status, debug),
		Debug:      debug,
	}
	if ctx != nil {
		data.RequestID = ctx.RequestID
		data.TraceID = ctx.TraceID
		data.Method = ctx.Method
		data.URL = ctx.URL
		if !ctx.Timestamp.IsZero() {
			data.Timestamp = ctx.Timestamp.Format("2006-01-02 15:04:05")
		}
	}
	if debug {
		data.ErrorType = errorTypeName(err)
		data.Origin = originOf(err)
		data.Context = debugContext(err, ctx)
		if prev := errors.Unwrap(err); prev != nil {
			data.Previous = prev.Error()
		}
		if ctx != nil && ctx.StackTrace != nil {
			data.Frames = ctx.StackTrace.GetFramesWithSource(5)
		}
	}

	tpl := r.template(status, debug)
	if tpl == nil {
		return errors.Join(ErrInvalidTemplate, errBuiltinDebug, errBuiltinError)
	}
	var buf bytes.Buffer
	if execErr := tpl.Execute(&buf, data); execErr != nil {
		return execErr
	}
	rc.SetHeader("Content-Type", "text/html; charset=utf-8")
	rc.SetHeader("X-Content-Type-Options", "nosniff")
	rc.WriteHeader(status)
	_, writeErr := rc.Write(buf.Bytes())
	return writeErr
}

// templateFuncs are the helpers available to the built-in templates.
var templateFuncs = template.FuncMap{
	"add": func(a, b int) int { return a + b },
	"sub": func(a, b int) int { return a - b },
}
