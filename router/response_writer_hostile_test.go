package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// A commit listener may call back into the wrapper that runs it: the call
// does not run the listener again or wait on the dispatch in progress (a
// sync.Once gate waited on itself for good), and it does not replace the
// status the response was being committed with.
func TestResponseWriter_CommitListenerMayCallBackIntoTheWrapper(t *testing.T) {
	for _, first := range []struct {
		name  string
		write func(rw *responseWriter)
		want  int
	}{
		{"WriteHeader", func(rw *responseWriter) { rw.WriteHeader(http.StatusCreated) }, http.StatusCreated},
		{"Write", func(rw *responseWriter) { _, _ = rw.Write([]byte("body")) }, http.StatusOK},
		{"Flush", func(rw *responseWriter) { rw.Flush() }, http.StatusOK},
	} {
		t.Run(first.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rw := acquireResponseWriter(rec)
			defer releaseResponseWriter(rw)
			fired := 0
			got := 0
			rw.addListener(func(status int, w http.ResponseWriter) {
				fired++
				got = status
				w.Header().Set("X-Listener", "1")
				w.WriteHeader(http.StatusAccepted)
			})
			if p := hostile.Within(t, hostile.Deadline, func() { first.write(rw) }); p != nil {
				t.Fatalf("panicked: %v", p)
			}
			if fired != 1 {
				t.Fatalf("listener ran %d times, want 1", fired)
			}
			if got != first.want {
				t.Fatalf("listener got status %d, want %d", got, first.want)
			}
			if rec.Code != first.want || rec.Header().Get("X-Listener") != "1" {
				t.Fatalf("response = %d %v, want %d with the listener's header", rec.Code, rec.Header(), first.want)
			}
			if rw.Status() != first.want {
				t.Fatalf("recorded status = %d, want %d", rw.Status(), first.want)
			}
		})
	}
}
