package problem

import (
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/contract"
)

// ErrorContext carries the facts about where and how an error happened.
type ErrorContext = contract.ErrorContext

// Reporter receives reported errors.
type Reporter = contract.Reporter

// Conformance assertions for the concrete reporters.
var (
	_ contract.Reporter = (*LogReporter)(nil)
	_ contract.Reporter = (*CallbackReporter)(nil)
	_ contract.Reporter = (*MultiReporter)(nil)
)

// LogReporter writes reported errors to a contract.Logger at the level the
// pipeline selected (ErrorContext.Level; error when unset).
type LogReporter struct {
	logger      contract.Logger
	includeCtx  bool
	contextKeys []string
}

// LogReporterOption configures a LogReporter.
type LogReporterOption func(*LogReporter)

// NewLogReporter returns a LogReporter. Without WithLogger it reports
// nothing.
func NewLogReporter(opts ...LogReporterOption) *LogReporter {
	r := &LogReporter{includeCtx: true}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// WithLogger sets the logger the reporter writes to.
func WithLogger(logger contract.Logger) LogReporterOption {
	return func(r *LogReporter) { r.logger = logger }
}

// WithContextKeys limits the ErrorContext.Extra fields written to keys.
func WithContextKeys(keys ...string) LogReporterOption {
	return func(r *LogReporter) { r.contextKeys = append([]string(nil), keys...) }
}

// WithoutContext omits every ErrorContext field from the log entry.
func WithoutContext() LogReporterOption {
	return func(r *LogReporter) { r.includeCtx = false }
}

// Report logs err with its fields at ctx.Level.
func (r *LogReporter) Report(err error, ctx *ErrorContext) {
	if r.logger == nil || err == nil {
		return
	}
	fields := r.buildFields(err, ctx)
	level := contract.LogLevelUnset
	if ctx != nil {
		level = ctx.Level
	}
	switch level {
	case contract.LogLevelDebug:
		r.logger.Debug(err.Error(), fields...)
	case contract.LogLevelInfo:
		r.logger.Info(err.Error(), fields...)
	case contract.LogLevelWarn:
		r.logger.Warn(err.Error(), fields...)
	default:
		r.logger.Error(err.Error(), fields...)
	}
}

// buildFields returns the key-value pairs logged with err.
func (r *LogReporter) buildFields(err error, ctx *ErrorContext) []any {
	var fields []any
	if status, _, ok := contract.StatusOf(err); ok {
		fields = append(fields, "status", status)
	}
	if origin := originOf(err); origin != "" {
		fields = append(fields, "origin", origin)
	}
	if ctx == nil || !r.includeCtx {
		return fields
	}
	for _, kv := range []struct{ k, v string }{
		{"request_id", ctx.RequestID},
		{"trace_id", ctx.TraceID},
		{"span_id", ctx.SpanID},
		{"user_id", ctx.UserID},
		{"url", ctx.URL},
		{"method", ctx.Method},
		{"ip", ctx.IP},
		{"user_agent", ctx.UserAgent},
	} {
		if kv.v != "" {
			fields = append(fields, kv.k, kv.v)
		}
	}
	if ctx.Recovered {
		fields = append(fields, "recovered", true)
	}
	if ctx.PanicStack != "" {
		fields = append(fields, "stack", ctx.PanicStack)
	}
	if ctx.StackTrace != nil && len(ctx.StackTrace.Frames) > 0 {
		frame := ctx.StackTrace.Frames[0]
		fields = append(fields, "file", fmt.Sprintf("%s:%d", frame.File, frame.Line), "function", frame.Function)
	}
	if len(r.contextKeys) > 0 {
		for _, k := range r.contextKeys {
			if v, ok := ctx.Extra[k]; ok {
				fields = append(fields, k, v)
			}
		}
		return fields
	}
	for k, v := range ctx.Extra {
		fields = append(fields, k, v)
	}
	return fields
}

// CallbackReporter reports errors through a callback.
type CallbackReporter struct {
	callback func(err error, ctx *ErrorContext)
}

// NewCallbackReporter returns a CallbackReporter for callback.
func NewCallbackReporter(callback func(err error, ctx *ErrorContext)) *CallbackReporter {
	return &CallbackReporter{callback: callback}
}

// Report calls the callback.
func (r *CallbackReporter) Report(err error, ctx *ErrorContext) {
	if r.callback != nil {
		r.callback(err, ctx)
	}
}

// MultiReporter fans a report out to several reporters. It is safe for
// concurrent use.
type MultiReporter struct {
	mu        sync.RWMutex
	reporters []Reporter
}

// NewMultiReporter returns a MultiReporter over reporters.
func NewMultiReporter(reporters ...Reporter) *MultiReporter {
	return &MultiReporter{reporters: append([]Reporter(nil), reporters...)}
}

// Report sends the error to every reporter.
func (r *MultiReporter) Report(err error, ctx *ErrorContext) {
	r.mu.RLock()
	reporters := r.reporters
	r.mu.RUnlock()
	for _, reporter := range reporters {
		reporter.Report(err, ctx)
	}
}

// AddReporter appends a reporter.
func (r *MultiReporter) AddReporter(reporter Reporter) {
	if reporter == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reporters = appendCopy(r.reporters, reporter)
}
