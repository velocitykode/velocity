package problem

import (
	"net/http"
	"testing"

	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/hostile"
)

// methodsPanic is an error whose Error, Is, As and Unwrap methods panic.
type methodsPanic struct{}

func (methodsPanic) Error() string { panic("Error broke") }
func (methodsPanic) Is(error) bool { panic("Is broke") }
func (methodsPanic) As(any) bool   { panic("As broke") }
func (methodsPanic) Unwrap() error { panic("Unwrap broke") }

// chainLoop unwraps to itself.
type chainLoop struct{}

func (e *chainLoop) Error() string { return "loop" }
func (e *chainLoop) Unwrap() error { return e }

// A request error whose methods panic, or whose chain loops, is handled:
// HandleRequest returns, the request is answered with the plain 500, and
// the error is reported once, at level error, where its text cannot be
// read the report carries the fixed text.
func TestHandleRequest_HostileErrorIsReportedAndAnswered(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"methods panic", methodsPanic{}},
		{"loop", &chainLoop{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, rep, _ := newTestHandler()
			rc, w := newRC(http.MethodGet, "/x")
			if p := hostile.Within(t, hostile.Deadline, func() {
				h.HandleRequest(rc, tc.err, nil)
			}); p != nil {
				t.Fatalf("HandleRequest panicked: %v", p)
			}
			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", w.Code)
			}
			if n := rep.count(); n != 1 {
				t.Errorf("reports = %d, want 1", n)
			}
		})
	}
}

// The log reporter writes a report whose error text cannot be read with
// the fixed text, and does not panic.
func TestLogReporter_UnreadableErrorText(t *testing.T) {
	logger := &recLogger{}
	rep := NewLogReporter(WithLogger(logger))
	if p := hostile.Within(t, hostile.Deadline, func() { rep.Report(methodsPanic{}, nil) }); p != nil {
		t.Fatalf("Report panicked: %v", p)
	}
	entries := logger.all()
	if len(entries) != 1 || entries[0].msg != errchain.Unreadable {
		t.Fatalf("log entries = %+v, want one with the fixed text", entries)
	}
}
