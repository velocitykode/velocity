package problem

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestHandleConsole(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		debug      bool
		wantCode   int
		wantOut    string
		wantReport int
	}{
		{name: "Nil", err: nil, wantCode: 0, wantOut: "", wantReport: 0},
		{name: "PlainError", err: errors.New("migrate: connection refused"), wantCode: 1, wantOut: "error: migrate: connection refused\n", wantReport: 1},
		{name: "ExitCoder", err: fmt.Errorf("wrap: %w", &exitErr{code: 3}), wantCode: 3, wantOut: "error: wrap: exit\n", wantReport: 1},
		{name: "ExitCoderZero", err: &exitErr{code: 0}, wantCode: 0, wantOut: "error: exit\n", wantReport: 1},
		{name: "ExitCoderNegative", err: &exitErr{code: -2}, wantCode: 1, wantOut: "error: exit\n", wantReport: 1},
		{name: "ClientStatusMessageNotReported", err: NotFound("no such tenant"), wantCode: 1, wantOut: "error: no such tenant\n", wantReport: 0},
		{name: "ServerStatusWrappedHidesCause", err: Internal("seed failed").WithCause(errors.New("secret dsn")), wantCode: 1, wantOut: "error: seed failed\n", wantReport: 1},
		{name: "StatusErrorTitle", err: &statusErr{code: 503}, wantCode: 1, wantOut: "error: Service Unavailable\n", wantReport: 1},
		{name: "DebugFullError", err: Internal("seed failed").WithCause(errors.New("secret dsn")), debug: true, wantCode: 1, wantOut: "error: seed failed: secret dsn\n", wantReport: 1},
		{name: "MultiLineCollapsed", err: errors.New("line one\nline two"), wantCode: 1, wantOut: "error: line one line two\n", wantReport: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, _ := newTestHandler(WithDebug(tt.debug))
			var out bytes.Buffer
			if code := h.HandleConsole(&out, tt.err); code != tt.wantCode {
				t.Errorf("code = %d, want %d", code, tt.wantCode)
			}
			if out.String() != tt.wantOut {
				t.Errorf("stderr = %q, want %q", out.String(), tt.wantOut)
			}
			if rep.count() != tt.wantReport {
				t.Errorf("reports = %d, want %d", rep.count(), tt.wantReport)
			}
		})
	}
}

func TestHandleConsole_NilWriterAndIgnoreRules(t *testing.T) {
	h, rep, _ := newTestHandler()
	IgnoreIs(h, errSentinel)
	if code := h.HandleConsole(nil, errSentinel); code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if rep.count() != 0 {
		t.Errorf("ignored console error reported %d times", rep.count())
	}
}
