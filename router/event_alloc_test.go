package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// nopWriter is a ResponseWriter that allocates nothing per request.
type nopWriter struct{ header http.Header }

func (w *nopWriter) Header() http.Header         { return w.header }
func (w *nopWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *nopWriter) WriteHeader(int)             {}

// TestServeHTTP_NoDispatcherBuildsNoEvent serves the matched, unmatched and
// static paths with no event dispatcher installed and holds each to the
// allocations of the request itself: an event struct built for no
// listener would add one allocation per event (RequestStarted and
// RequestHandled on every path, RequestRouted on the unmatched and static
// ones).
func TestServeHTTP_NoDispatcherBuildsNoEvent(t *testing.T) {
	if raceEnabled {
		t.Skip("sync.Pool drops items at random under the race detector, so allocation counts are not exact")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.css"), []byte("body{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New()
	r.Static(dir)
	r.Get("/users/{id}", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})
	r.Freeze()

	// Budgets are the request's own allocations, measured with Go 1.26:
	// with the events built regardless the three paths allocated 18, 18
	// and 36 times.
	tests := []struct {
		name   string
		path   string
		budget float64
	}{
		{"matched", "/users/42", 16},
		{"unmatched", "/nowhere", 15},
		{"static", "/app.css", 33},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			w := &nopWriter{header: http.Header{}}
			allocs := testing.AllocsPerRun(200, func() {
				clear(w.header)
				r.ServeHTTP(w, req)
			})
			if allocs > tt.budget {
				t.Errorf("ServeHTTP(%s) with no dispatcher allocated %.0f times, want at most %.0f", tt.path, allocs, tt.budget)
			}
		})
	}
}
