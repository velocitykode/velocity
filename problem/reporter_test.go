package problem

import (
	"errors"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

func TestLogReporter_Levels(t *testing.T) {
	tests := []struct {
		level contract.LogLevel
		want  string
	}{
		{contract.LogLevelUnset, "error"},
		{contract.LogLevelDebug, "debug"},
		{contract.LogLevelInfo, "info"},
		{contract.LogLevelWarn, "warn"},
		{contract.LogLevelError, "error"},
	}
	for _, tt := range tests {
		t.Run(tt.want+tt.level.String(), func(t *testing.T) {
			logger := &recLogger{}
			NewLogReporter(WithLogger(logger)).Report(errors.New("boom"), &ErrorContext{Level: tt.level})
			entries := logger.all()
			if len(entries) != 1 || entries[0].level != tt.want || entries[0].msg != "boom" {
				t.Errorf("entries = %+v, want one %s entry", entries, tt.want)
			}
		})
	}
}

func TestLogReporter_Fields(t *testing.T) {
	ctx := &ErrorContext{
		RequestID: "r", TraceID: "t", SpanID: "s", UserID: "u", URL: "/x", Method: "GET",
		IP: "1.2.3.4", UserAgent: "ua", Recovered: true, PanicStack: "goroutine 1",
		StackTrace: &contract.StackTrace{Frames: []contract.StackFrame{{File: "/a.go", Line: 3, Function: "F"}}},
		Extra:      map[string]any{"tenant": "acme", "noise": 1},
	}
	tests := []struct {
		name    string
		opts    []LogReporterOption
		err     error
		present map[string]any
		absent  []string
	}{
		{
			name: "AllFields",
			err:  NotFound(),
			present: map[string]any{
				"status": 404, "request_id": "r", "trace_id": "t", "span_id": "s", "user_id": "u",
				"url": "/x", "method": "GET", "ip": "1.2.3.4", "user_agent": "ua", "recovered": true,
				"stack": "goroutine 1", "file": "/a.go:3", "function": "F", "tenant": "acme", "noise": 1,
			},
		},
		{
			name:    "ContextKeysFilterExtra",
			opts:    []LogReporterOption{WithContextKeys("tenant", "missing")},
			err:     errors.New("plain"),
			present: map[string]any{"tenant": "acme", "request_id": "r"},
			absent:  []string{"noise", "missing", "status"},
		},
		{
			name:    "WithoutContext",
			opts:    []LogReporterOption{WithoutContext()},
			err:     NotFound(),
			present: map[string]any{"status": 404},
			absent:  []string{"request_id", "tenant", "stack"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &recLogger{}
			NewLogReporter(append([]LogReporterOption{WithLogger(logger)}, tt.opts...)...).Report(tt.err, ctx)
			entry := logger.all()[0]
			for k, v := range tt.present {
				if got := entry.field(k); got != v {
					t.Errorf("field %q = %v, want %v", k, got, v)
				}
			}
			for _, k := range tt.absent {
				if got := entry.field(k); got != nil {
					t.Errorf("field %q = %v, want absent", k, got)
				}
			}
			if origin, _ := entry.field("origin").(string); tt.name == "AllFields" && origin == "" {
				t.Error("origin missing for a constructed HTTPError")
			}
		})
	}
}

func TestLogReporter_NoLoggerOrNilError(t *testing.T) {
	NewLogReporter().Report(errors.New("dropped"), nil)
	logger := &recLogger{}
	r := NewLogReporter(WithLogger(logger))
	r.Report(nil, nil)
	r.Report(errors.New("nil ctx"), nil)
	if n := len(logger.all()); n != 1 {
		t.Errorf("entries = %d, want 1", n)
	}
}

func TestCallbackAndMultiReporter(t *testing.T) {
	var got []string
	var mu sync.Mutex
	cb := NewCallbackReporter(func(err error, _ *ErrorContext) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, err.Error())
	})
	NewCallbackReporter(nil).Report(errors.New("no callback"), nil)

	multi := NewMultiReporter(cb)
	multi.AddReporter(nil)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); multi.AddReporter(cb) }()
		go func() { defer wg.Done(); multi.Report(errors.New("x"), nil) }()
	}
	wg.Wait()
	multi.Report(errors.New("final"), nil)
	mu.Lock()
	defer mu.Unlock()
	if got[len(got)-1] != "final" || len(got) < 5 {
		t.Errorf("reports = %v", got)
	}
}
