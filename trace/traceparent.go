package trace

import "context"

// TraceparentHeader is the W3C Trace Context header that carries a caller's
// trace id and span id across a process edge. gRPC metadata uses the same
// lowercase key.
const TraceparentHeader = "traceparent"

const (
	// traceparentLen is the exact length of a version 00 value:
	// 2 (version) + 1 + 32 (trace id) + 1 + 16 (parent id) + 1 + 2 (flags).
	traceparentLen = 55

	// maxTraceparentLen bounds a later-version value, which may append
	// fields after the version 00 layout. Longer input is rejected before
	// it is scanned.
	maxTraceparentLen = 512
)

// Parent is the span a new span continues from across a process edge: the
// trace it belongs to and the caller's span within it. It is what a
// traceparent header carries.
type Parent struct {
	// TraceID is the trace the caller's span belongs to.
	TraceID string
	// SpanID is the caller's span, recorded as the ParentID of the span
	// started from this Parent.
	SpanID string
	// Sampled is the sampled bit of the header's trace-flags. The other
	// flag bits are dropped on parse and never written.
	Sampled bool
}

// ParseTraceparent parses a W3C traceparent header value. It accepts
// version 00 (exactly "00-<trace-id>-<parent-id>-<flags>") and, as the
// specification requires, a later version whose value starts with that
// layout followed by the end of the value or a dash. The trace id (32) and
// parent id (16) must be lowercase hex and not all zeros, the flags two
// lowercase hex digits, and version ff is invalid. Values longer than 512
// bytes are rejected unread.
//
// The value comes from the network: ok is false for anything malformed, and
// the caller then starts a root span instead of continuing a trace.
func ParseTraceparent(value string) (p Parent, ok bool) {
	if len(value) < traceparentLen || len(value) > maxTraceparentLen {
		return Parent{}, false
	}
	version := value[0:2]
	if !isLowerHex(version) || version == "ff" {
		return Parent{}, false
	}
	if version == "00" && len(value) != traceparentLen {
		return Parent{}, false
	}
	if len(value) > traceparentLen && value[traceparentLen] != '-' {
		return Parent{}, false
	}
	if value[2] != '-' || value[35] != '-' || value[52] != '-' {
		return Parent{}, false
	}
	traceID, spanID, flags := value[3:35], value[36:52], value[53:55]
	if !isW3CID(traceID) || !isW3CID(spanID) || !isLowerHex(flags) {
		return Parent{}, false
	}
	return Parent{TraceID: traceID, SpanID: spanID, Sampled: hexValue(flags[1])&0x1 == 1}, true
}

// FormatTraceparent formats p as a version 00 traceparent header value.
// ok is false when p's ids cannot travel in the header: a trace id that is
// not 32 lowercase hex characters, a span id that is not 16, or either all
// zeros. That covers the fallback ids minted while the entropy source is
// unavailable, which are deliberately not hex.
func FormatTraceparent(p Parent) (value string, ok bool) {
	if len(p.TraceID) != 32 || len(p.SpanID) != 16 || !isW3CID(p.TraceID) || !isW3CID(p.SpanID) {
		return "", false
	}
	flags := "00"
	if p.Sampled {
		flags = "01"
	}
	return "00-" + p.TraceID + "-" + p.SpanID + "-" + flags, true
}

// Propagate writes the carriers an outbound call sends from ctx through
// set: TraceparentHeader naming ctx's trace and current span as the
// callee's parent, and RequestIDHeader with ctx's request id. The
// traceparent is always sampled: the framework does not sample, every span
// it starts is reported.
//
// A header is skipped when ctx has no value for it or its value cannot
// travel (ids FormatTraceparent rejects, a request id ValidRequestID
// rejects), so propagating never makes an outbound call fail.
func Propagate(ctx context.Context, set func(name, value string)) {
	if ctx == nil || set == nil {
		return
	}
	if value, ok := FormatTraceparent(Parent{TraceID: GetTraceID(ctx), SpanID: GetSpanID(ctx), Sampled: true}); ok {
		set(TraceparentHeader, value)
	}
	if id := GetRequestID(ctx); ValidRequestID(id) {
		set(RequestIDHeader, id)
	}
}

// isW3CID reports whether s is lowercase hex and not all zeros, the shape
// W3C Trace Context requires of a trace id and a parent id.
func isW3CID(s string) bool {
	if !isLowerHex(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return true
		}
	}
	return false
}

// isLowerHex reports whether s is non-empty and every byte is 0-9 or a-f.
func isLowerHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// hexValue returns the value of one lowercase hex digit. Callers check the
// digit with isLowerHex first.
func hexValue(c byte) byte {
	if c >= 'a' {
		return c - 'a' + 10
	}
	return c - '0'
}
