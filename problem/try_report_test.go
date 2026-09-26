package problem

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// TestHandler_TryReport asserts TryReport answers for the report it makes:
// true exactly when the error reached the reporters, including where the
// answer differs from ShouldReport on the error as passed (a user map rule
// applied before the gate, throttling).
func TestHandler_TryReport(t *testing.T) {
	errServer := errors.New("server exploded")
	errMapped := errors.New("mapped away")
	tests := []struct {
		name       string
		setup      func(h *Handler)
		err        func() error
		calls      int // TryReport calls with a fresh err each time
		want       []bool
		wantShould bool // ShouldReport on the first err
	}{
		{name: "plain error", err: func() error { return errors.New("boom") }, calls: 1, want: []bool{true}, wantShould: true},
		{name: "nil", err: func() error { return nil }, calls: 1, want: []bool{false}, wantShould: false},
		{name: "already reported", err: func() error { return contract.MarkReported(errors.New("boom")) }, calls: 1, want: []bool{false}, wantShould: false},
		{name: "client status", err: func() error { return contract.NewHTTPError(404) }, calls: 1, want: []bool{false}, wantShould: false},
		{
			name:       "map rule turns a client status into a server error",
			setup:      func(h *Handler) { MapFor[*contract.HTTPError](h, func(*contract.HTTPError) error { return errServer }) },
			err:        func() error { return contract.NewHTTPError(404) },
			calls:      1,
			want:       []bool{true},
			wantShould: false,
		},
		{
			name: "map rule turns a server error into an ignored one",
			setup: func(h *Handler) {
				MapIs(h, errServer, func(error) error { return errMapped })
				IgnoreIs(h, errMapped)
			},
			err:        func() error { return errServer },
			calls:      1,
			want:       []bool{false},
			wantShould: true,
		},
		{
			name: "throttled after the first report",
			setup: func(h *Handler) {
				ThrottleFor[*throttledErr](h, contract.Throttle{MaxPerWindow: 1, Window: time.Hour})
			},
			err:        func() error { return &throttledErr{} },
			calls:      2,
			want:       []bool{true, false},
			wantShould: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reports atomic.Int32
			h := NewHandler(WithReporters(NewCallbackReporter(func(error, *ErrorContext) { reports.Add(1) })))
			if tt.setup != nil {
				tt.setup(h)
			}
			if got := h.ShouldReport(tt.err()); got != tt.wantShould {
				t.Errorf("ShouldReport = %v, want %v", got, tt.wantShould)
			}
			wantReports := int32(0)
			for i := 0; i < tt.calls; i++ {
				got := h.TryReport(tt.err(), nil)
				if got != tt.want[i] {
					t.Errorf("TryReport call %d = %v, want %v", i+1, got, tt.want[i])
				}
				if tt.want[i] {
					wantReports++
				}
			}
			if got := reports.Load(); got != wantReports {
				t.Errorf("reporters received %d reports, want %d (TryReport answers must match)", got, wantReports)
			}
		})
	}
}

// throttledErr is a server error type a throttle rule can match.
type throttledErr struct{}

func (*throttledErr) Error() string { return "throttled" }

// panickingContextErr is a server error whose Context panics.
type panickingContextErr struct{}

func (*panickingContextErr) Error() string           { return "context exploded" }
func (*panickingContextErr) Context() map[string]any { panic("context") }

// panickingSelfErr is an error whose ReportError panics.
type panickingSelfErr struct{}

func (*panickingSelfErr) Error() string                  { return "self report exploded" }
func (*panickingSelfErr) ReportError(*ErrorContext) bool { panic("self report") }

// TestHandler_TryReport_PanicBeforeHandledIsNotReported asserts TryReport
// answers true only once the report was handled: taken over by a
// SelfReporting error or a ReportFor rule, or handed to the reporters (a
// panicking reporter included, the others still run). A panic that ends
// the report before any of those points answers false, reaches no
// reporter, and is logged as a failed report.
func TestHandler_TryReport_PanicBeforeHandledIsNotReported(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(h *Handler, rep *recReporter)
		err         error
		want        bool
		wantReports int
		wantLog     string
	}{
		{
			name:    "Contextual error whose Context panics",
			err:     &panickingContextErr{},
			wantLog: "problem: report failed",
		},
		{
			name: "ContextUsing provider panics",
			setup: func(h *Handler, _ *recReporter) {
				h.ContextUsing(func(error, *ErrorContext) map[string]any { panic("provider") })
			},
			err:     errors.New("boom"),
			wantLog: "problem: report failed",
		},
		{
			name: "level rule panics",
			setup: func(h *Handler, _ *recReporter) {
				h.AddLevelRule(contract.LevelRule{Match: func(error) bool { panic("level") }, Level: contract.LogLevelWarn})
			},
			err:     errors.New("boom"),
			wantLog: "problem: report failed",
		},
		{
			name: "ReportFor rule panics",
			setup: func(h *Handler, _ *recReporter) {
				ReportFor(h, func(*contextualErr, *ErrorContext) bool { panic("rule") })
			},
			err:     &contextualErr{},
			wantLog: "problem: report failed",
		},
		{
			name:    "SelfReporting error panics",
			err:     &panickingSelfErr{},
			wantLog: "problem: report failed",
		},
		{
			name: "SelfReporting error handles it",
			err:  &selfErr{stop: true},
			want: true,
		},
		{
			name: "ReportFor rule handles it",
			setup: func(h *Handler, _ *recReporter) {
				ReportFor(h, func(*contextualErr, *ErrorContext) bool { return true })
			},
			err:  &contextualErr{},
			want: true,
		},
		{
			name: "a reporter panics, the next still runs",
			setup: func(h *Handler, rep *recReporter) {
				h.SetReporters(NewCallbackReporter(func(error, *ErrorContext) { panic("reporter") }), rep)
			},
			err:         errors.New("boom"),
			want:        true,
			wantReports: 1,
			wantLog:     "problem: reporter panicked",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, logger := newTestHandler()
			if tt.setup != nil {
				tt.setup(h, rep)
			}
			if got := h.TryReport(tt.err, nil); got != tt.want {
				t.Errorf("TryReport = %v, want %v", got, tt.want)
			}
			if got := rep.count(); got != tt.wantReports {
				t.Errorf("reporter calls = %d, want %d", got, tt.wantReports)
			}
			if tt.wantLog != "" && !logger.has("error", tt.wantLog) {
				t.Errorf("missing error log %q in %v", tt.wantLog, logger.all())
			}
			if tt.wantLog == "" && logger.has("error", "problem: report failed") {
				t.Errorf("unexpected failed-report log in %v", logger.all())
			}
		})
	}
}
