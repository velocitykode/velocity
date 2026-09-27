package trace

import "context"

// LogFields returns the request, trace and span ids ctx carries as log
// key-value pairs, request_id, trace_id and span_id in that order, leaving
// out each id ctx does not carry. Reading them generates a lazy request or
// trace id. Bind them to a logger with contract.Logger's With so every line
// written under ctx names the same ids as the reports made under it (see
// NewErrorContext).
func LogFields(ctx context.Context) []any {
	if ctx == nil {
		return nil
	}
	fields := make([]any, 0, 6)
	if id := GetRequestID(ctx); id != "" {
		fields = append(fields, "request_id", id)
	}
	if id := GetTraceID(ctx); id != "" {
		fields = append(fields, "trace_id", id)
	}
	if id := GetSpanID(ctx); id != "" {
		fields = append(fields, "span_id", id)
	}
	return fields
}
