package problem

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// refusingWriter reports its own commitment and can refuse a status
// write, as the router's writer does for a write made while its commit
// listeners run.
type refusingWriter struct {
	*httptest.ResponseRecorder
	refuse    bool
	committed bool
}

func (w *refusingWriter) WriteHeader(code int) {
	if w.refuse {
		return
	}
	w.committed = true
	w.ResponseRecorder.WriteHeader(code)
}

func (w *refusingWriter) Write(p []byte) (int, error) {
	if w.refuse {
		return 0, http.ErrBodyNotAllowed
	}
	w.committed = true
	return w.ResponseRecorder.Write(p)
}

func (w *refusingWriter) Committed() bool { return w.committed }

// Over a writer that reports its own commitment, a TrackedWriter says what
// that writer says: a status or body write the writer refused is not a
// commitment.
func TestTrackedWriter_ReadsCommitmentFromAWriterThatReportsIt(t *testing.T) {
	under := &refusingWriter{ResponseRecorder: httptest.NewRecorder(), refuse: true}
	tw := NewTrackedWriter(under)
	tw.WriteHeader(http.StatusNoContent)
	_, _ = tw.Write([]byte("x"))
	if tw.Committed() {
		t.Fatal("Committed = true after writes the writer underneath refused")
	}
	under.refuse = false
	tw.WriteHeader(http.StatusOK)
	if !tw.Committed() {
		t.Fatal("Committed = false after the writer underneath took the status")
	}
}
