package bond

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/trace"
)

// flushFieldLogger records the pairs, bound ones first, of its last Warn.
type flushFieldLogger struct {
	mu    *sync.Mutex
	last  *[]any
	bound []any
}

func (l flushFieldLogger) Debug(string, ...any) {}
func (l flushFieldLogger) Info(string, ...any)  {}
func (l flushFieldLogger) Error(string, ...any) {}
func (l flushFieldLogger) Fatal(string, ...any) {}
func (l flushFieldLogger) Warn(_ string, kvs ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.last = append(append([]any(nil), l.bound...), kvs...)
}
func (l flushFieldLogger) With(kvs ...any) contract.Logger {
	return flushFieldLogger{mu: l.mu, last: l.last, bound: append(append([]any(nil), l.bound...), kvs...)}
}

// failingWriter accepts headers and fails every body write.
type failingWriter struct{ *httptest.ResponseRecorder }

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// The warning for a buffered response that could not be flushed names the
// request's path under url and carries the request, trace and span ids.
func TestMiddleware_FlushFailureLineCarriesTheRequestIDs(t *testing.T) {
	b := setupBond(t)
	var mu sync.Mutex
	var last []any
	b.SetLogger(flushFieldLogger{mu: &mu, last: &last})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("body")) })
	ctx := trace.WithRequestID(trace.WithTrace(context.Background(), "t1", "s1"), "r1")
	r := httptest.NewRequest(http.MethodGet, "/dashboard", nil).WithContext(ctx)
	r.Header.Set("X-Inertia", "true")

	b.Middleware(next).ServeHTTP(failingWriter{httptest.NewRecorder()}, r)

	mu.Lock()
	defer mu.Unlock()
	fields := map[any]any{}
	for i := 0; i+1 < len(last); i += 2 {
		fields[last[i]] = last[i+1]
	}
	for key, want := range map[string]string{"url": "/dashboard", "request_id": "r1", "trace_id": "t1", "span_id": "s1"} {
		if got := fields[key]; got != want {
			t.Errorf("%s = %v, want %q (%v)", key, got, want, last)
		}
	}
	if _, ok := fields["path"]; ok {
		t.Errorf("line still carries path: %v", last)
	}
}
