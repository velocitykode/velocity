package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// A BeforeFirstWrite hook may write through the wrapper it is registered
// on: the write does not fire the hook again (a sync.Once gate waited on
// itself for good), and the write that fired the hook does not commit a
// second status.
func TestResponseWriter_BeforeFirstWriteHookMayWriteThroughTheWrapper(t *testing.T) {
	for _, first := range []struct {
		name  string
		write func(rw *responseWriter)
	}{
		{"WriteHeader", func(rw *responseWriter) { rw.WriteHeader(http.StatusOK) }},
		{"Write", func(rw *responseWriter) { _, _ = rw.Write([]byte("body")) }},
		{"Flush", func(rw *responseWriter) { rw.Flush() }},
	} {
		t.Run(first.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rw := acquireResponseWriter(rec)
			defer releaseResponseWriter(rw)
			fired := 0
			rw.BeforeFirstWrite(func() {
				fired++
				rw.Header().Set("X-Hook", "1")
				rw.WriteHeader(http.StatusAccepted)
			})
			if p := hostile.Within(t, hostile.Deadline, func() { first.write(rw) }); p != nil {
				t.Fatalf("panicked: %v", p)
			}
			if fired != 1 {
				t.Fatalf("hook fired %d times, want 1", fired)
			}
			if rec.Code != http.StatusAccepted || rec.Header().Get("X-Hook") != "1" {
				t.Fatalf("response = %d %v, want the hook's 202 with its header", rec.Code, rec.Header())
			}
			if rw.Status() != http.StatusAccepted {
				t.Fatalf("recorded status = %d, want 202", rw.Status())
			}
		})
	}
}
