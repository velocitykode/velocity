package contract

import (
	"testing"
)

func TestLogLevel_String(t *testing.T) {
	tests := []struct {
		level LogLevel
		want  string
	}{
		{LogLevelUnset, ""},
		{LogLevelDebug, "debug"},
		{LogLevelInfo, "info"},
		{LogLevelWarn, "warn"},
		{LogLevelError, "error"},
		{LogLevel(99), ""},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.level.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestErrorContext_Builders(t *testing.T) {
	st := &StackTrace{Frames: []StackFrame{{File: "a.go", Line: 1}}}
	c := (&ErrorContext{}).
		WithRequestInfo("POST", "/orders", "10.0.0.1", "agent/1").
		WithIDs("req-1", "trace-1").
		WithUserID("42").
		WithStackTrace(st).
		WithExtra("k", "v")

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"method", c.Method, "POST"},
		{"url", c.URL, "/orders"},
		{"ip", c.IP, "10.0.0.1"},
		{"user agent", c.UserAgent, "agent/1"},
		{"request id", c.RequestID, "req-1"},
		{"trace id", c.TraceID, "trace-1"},
		{"user id", c.UserID, "42"},
		{"stack trace", c.StackTrace, st},
		{"extra", c.Extra["k"], "v"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}
