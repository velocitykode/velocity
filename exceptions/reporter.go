package exceptions

import (
	"errors"
	"fmt"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/log"
)

// ErrorContext contains contextual information about where an exception occurred.
type ErrorContext = contract.ErrorContext

// NewErrorContext creates a new ErrorContext with the current timestamp.
func NewErrorContext() *ErrorContext {
	return &ErrorContext{
		Timestamp: time.Now(),
		Extra:     make(map[string]any),
	}
}

// Reporter is the interface for exception reporters.
type Reporter = contract.Reporter

// Conformance assertions for the concrete reporters.
var (
	_ contract.Reporter = (*LogReporter)(nil)
	_ contract.Reporter = (*CallbackReporter)(nil)
	_ contract.Reporter = (*MultiReporter)(nil)
)

// LogReporter reports exceptions to the log package.
type LogReporter struct {
	logger      log.Logger
	includeCtx  bool
	contextKeys []string
}

// NewLogReporter creates a new LogReporter.
func NewLogReporter(opts ...LogReporterOption) *LogReporter {
	r := &LogReporter{
		includeCtx: true,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// LogReporterOption is a functional option for LogReporter.
type LogReporterOption func(*LogReporter)

// WithLogger sets a custom logger for the reporter.
func WithLogger(logger log.Logger) LogReporterOption {
	return func(r *LogReporter) {
		r.logger = logger
	}
}

// WithContextKeys sets which context keys to include in logs.
func WithContextKeys(keys ...string) LogReporterOption {
	return func(r *LogReporter) {
		r.contextKeys = keys
	}
}

// WithoutContext disables context inclusion in logs.
func WithoutContext() LogReporterOption {
	return func(r *LogReporter) {
		r.includeCtx = false
	}
}

// Report logs an exception with its context.
func (r *LogReporter) Report(err error, ctx *ErrorContext) {
	logger := r.logger
	if logger == nil {
		return // No logger configured, skip reporting
	}

	fields := r.buildFields(err, ctx)
	logger.Error(err.Error(), fields...)
}

// buildFields builds log fields from the error and context.
func (r *LogReporter) buildFields(err error, ctx *ErrorContext) []any {
	var fields []any

	// Add exception-specific fields
	var exc Exception
	if errors.As(err, &exc) {
		fields = append(fields, "code", exc.GetCode())
		if prev := exc.GetPrevious(); prev != nil {
			fields = append(fields, "previous", prev.Error())
		}
		excCtx := exc.GetContext()
		for k, v := range excCtx {
			fields = append(fields, k, v)
		}
	}

	// Add HTTP-specific fields (HttpException and every type embedding it)
	var httpExc httpStatusError
	if errors.As(err, &httpExc) {
		fields = append(fields, "status_code", httpExc.GetStatusCode())
	}

	if ctx == nil || !r.includeCtx {
		return fields
	}

	// Add context fields
	if ctx.RequestID != "" {
		fields = append(fields, "request_id", ctx.RequestID)
	}
	if ctx.TraceID != "" {
		fields = append(fields, "trace_id", ctx.TraceID)
	}
	if ctx.UserID != "" {
		fields = append(fields, "user_id", ctx.UserID)
	}
	if ctx.URL != "" {
		fields = append(fields, "url", ctx.URL)
	}
	if ctx.Method != "" {
		fields = append(fields, "method", ctx.Method)
	}
	if ctx.IP != "" {
		fields = append(fields, "ip", ctx.IP)
	}
	if ctx.UserAgent != "" {
		fields = append(fields, "user_agent", ctx.UserAgent)
	}

	// Add stack trace summary
	if ctx.StackTrace != nil && len(ctx.StackTrace.Frames) > 0 {
		frame := ctx.StackTrace.Frames[0]
		fields = append(fields, "file", fmt.Sprintf("%s:%d", frame.File, frame.Line))
		fields = append(fields, "function", frame.Function)
	}

	// Add extra fields
	for k, v := range ctx.Extra {
		fields = append(fields, k, v)
	}

	return fields
}

// CallbackReporter reports exceptions using a callback function.
type CallbackReporter struct {
	callback func(err error, ctx *ErrorContext)
}

// NewCallbackReporter creates a new CallbackReporter.
func NewCallbackReporter(callback func(err error, ctx *ErrorContext)) *CallbackReporter {
	return &CallbackReporter{callback: callback}
}

// Report calls the callback function with the error and context.
func (r *CallbackReporter) Report(err error, ctx *ErrorContext) {
	if r.callback != nil {
		r.callback(err, ctx)
	}
}

// MultiReporter reports to multiple reporters.
type MultiReporter struct {
	reporters []Reporter
}

// NewMultiReporter creates a new MultiReporter.
func NewMultiReporter(reporters ...Reporter) *MultiReporter {
	return &MultiReporter{reporters: reporters}
}

// Report sends the error to all reporters.
func (r *MultiReporter) Report(err error, ctx *ErrorContext) {
	for _, reporter := range r.reporters {
		reporter.Report(err, ctx)
	}
}

// AddReporter adds a reporter to the multi-reporter.
func (r *MultiReporter) AddReporter(reporter Reporter) {
	r.reporters = append(r.reporters, reporter)
}
