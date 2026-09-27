package trace

import (
	"context"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// NewErrorContext returns the ErrorContext for a failure that happened
// under ctx: stamped with the current time, carrying the request, trace and
// span ids ctx holds (a lazy request or trace id is generated here) and an
// empty Extra map. Every ErrorContext the framework builds starts here, so
// a report carries the ids of the log lines written under the same ctx.
// A nil ctx yields no ids.
func NewErrorContext(ctx context.Context) *contract.ErrorContext {
	ec := &contract.ErrorContext{Timestamp: time.Now(), Extra: make(map[string]any)}
	if ctx != nil {
		ec.RequestID = GetRequestID(ctx)
		ec.TraceID = GetTraceID(ctx)
		ec.SpanID = GetSpanID(ctx)
	}
	return ec
}
