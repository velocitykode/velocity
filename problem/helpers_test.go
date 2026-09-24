package problem

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/velocitykode/velocity/contract"
)

// logEntry is one call recorded by recLogger.
type logEntry struct {
	level string
	msg   string
	kvs   []any
}

// recLogger is a contract.Logger that records every call.
type recLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

func (l *recLogger) add(level, msg string, kvs []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, logEntry{level: level, msg: msg, kvs: kvs})
}

func (l *recLogger) Debug(msg string, kvs ...any) { l.add("debug", msg, kvs) }
func (l *recLogger) Info(msg string, kvs ...any)  { l.add("info", msg, kvs) }
func (l *recLogger) Warn(msg string, kvs ...any)  { l.add("warn", msg, kvs) }
func (l *recLogger) Error(msg string, kvs ...any) { l.add("error", msg, kvs) }
func (l *recLogger) Fatal(msg string, kvs ...any) { l.add("fatal", msg, kvs) }

func (l *recLogger) all() []logEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]logEntry(nil), l.entries...)
}

// has reports whether an entry at level has exactly msg.
func (l *recLogger) has(level, msg string) bool {
	for _, e := range l.all() {
		if e.level == level && e.msg == msg {
			return true
		}
	}
	return false
}

// field returns the value logged under key in e, or nil.
func (e logEntry) field(key string) any {
	for i := 0; i+1 < len(e.kvs); i += 2 {
		if e.kvs[i] == key {
			return e.kvs[i+1]
		}
	}
	return nil
}

// recReporter records reported errors and contexts.
type recReporter struct {
	mu   sync.Mutex
	errs []error
	ctxs []*ErrorContext
}

func (r *recReporter) Report(err error, ctx *ErrorContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
	r.ctxs = append(r.ctxs, ctx)
}

func (r *recReporter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errs)
}

func (r *recReporter) last() (*ErrorContext, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.errs) == 0 {
		return nil, nil
	}
	return r.ctxs[len(r.ctxs)-1], r.errs[len(r.errs)-1]
}

// newTestHandler returns a handler with a recording reporter and logger.
func newTestHandler(opts ...Option) (*Handler, *recReporter, *recLogger) {
	rep := &recReporter{}
	logger := &recLogger{}
	all := append([]Option{WithHandlerLogger(logger), WithReporters(rep), WithEnvironment("testing")}, opts...)
	return NewHandler(all...), rep, logger
}

// newRC returns a RenderContext over a recorder for method and path with
// the given request headers (key, value pairs).
func newRC(method, path string, headers ...string) (RenderContext, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(method, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	return contract.NewRenderContext(w, r), w
}

// newRCWithContext is newRC with the request bound to ctx.
func newRCWithContext(ctx context.Context, method, path string) (RenderContext, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(method, path, nil).WithContext(ctx)
	w := httptest.NewRecorder()
	return contract.NewRenderContext(w, r), w
}

// statusErr names a status but has no ShouldReport.
type statusErr struct{ code int }

func (e *statusErr) Error() string   { return "status error " + http.StatusText(e.code) }
func (e *statusErr) StatusCode() int { return e.code }

// reportableErr decides its own reporting and names a status.
type reportableErr struct {
	report bool
	code   int
}

func (e *reportableErr) Error() string      { return "reportable" }
func (e *reportableErr) ShouldReport() bool { return e.report }
func (e *reportableErr) StatusCode() int    { return e.code }

// selfErr reports itself.
type selfErr struct {
	stop bool
	mu   sync.Mutex
	hits int
}

func (e *selfErr) Error() string { return "self reporting" }
func (e *selfErr) ReportError(*ErrorContext) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hits++
	return e.stop
}

// contextualErr contributes report fields.
type contextualErr struct{ fields map[string]any }

func (e *contextualErr) Error() string           { return "contextual" }
func (e *contextualErr) Context() map[string]any { return e.fields }

// renderableErr renders itself.
type renderableErr struct{ handled bool }

func (e *renderableErr) Error() string { return "renderable" }
func (e *renderableErr) RenderError(rc RenderContext, _ *ErrorContext) bool {
	if !e.handled {
		return false
	}
	rc.SetHeader("Content-Type", "text/plain")
	rc.WriteHeader(http.StatusTeapot)
	_, _ = rc.Write([]byte("self rendered"))
	return true
}

// fieldsErr is a 422 carrying field errors.
type fieldsErr struct{ fields map[string][]string }

func (e *fieldsErr) Error() string               { return "validation failed: email taken" }
func (e *fieldsErr) StatusCode() int             { return http.StatusUnprocessableEntity }
func (e *fieldsErr) Errors() map[string][]string { return e.fields }

// typedErr names its problem type.
type typedErr struct{}

func (typedErr) Error() string       { return "typed" }
func (typedErr) StatusCode() int     { return http.StatusConflict }
func (typedErr) ProblemType() string { return "https://example.test/problems/conflict" }

// exitErr names an exit code.
type exitErr struct{ code int }

func (e *exitErr) Error() string { return "exit" }
func (e *exitErr) ExitCode() int { return e.code }

// panicRenderer panics on Render.
type panicRenderer struct{}

func (panicRenderer) Render(RenderContext, error, *ErrorContext, int, bool) error {
	panic("renderer exploded")
}
func (panicRenderer) ContentType() string { return "text/html" }

// failRenderer returns an error without writing.
type failRenderer struct{}

func (failRenderer) Render(RenderContext, error, *ErrorContext, int, bool) error {
	return http.ErrBodyNotAllowed
}
func (failRenderer) ContentType() string { return "text/html" }
