package routerbridge

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// hijackRecorder is a ResponseRecorder whose Hijack succeeds, so a
// handler can take the connection over through the router's writer.
type hijackRecorder struct {
	*httptest.ResponseRecorder
}

func (hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return c1, bufio.NewReadWriter(bufio.NewReader(c1), bufio.NewWriter(c1)), nil
}

// problemEntries are the two ways a handler hands an error to the problem
// pipeline over a writer it was given: problem.ErrorHandler with a
// returned error, and problem.Middleware recovering a panic. Each runs
// commit on the writer first.
var problemEntries = []struct {
	name  string
	route func(h contract.ErrorHandler, commit func(http.ResponseWriter)) router.HandlerFunc
}{
	{
		name: "ErrorHandler",
		route: func(h contract.ErrorHandler, commit func(http.ResponseWriter)) router.HandlerFunc {
			return func(c *router.Context) error {
				commit(c.Response)
				problem.ErrorHandler(h)(c.Response, c.Request, errors.New("late failure"))
				return nil
			}
		},
	},
	{
		name: "Middleware",
		route: func(h contract.ErrorHandler, commit func(http.ResponseWriter)) router.HandlerFunc {
			return func(c *router.Context) error {
				problem.Middleware(h)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					commit(w)
					panic("late failure")
				})).ServeHTTP(c.Response, c.Request)
				return nil
			}
		},
	},
}

// TestProblemOverRouterWriter_SeesCommitment asserts problem.ErrorHandler
// and problem.Middleware, given the router's writer, see a response the
// handler committed (a final status or a hijack): the error is reported
// and nothing is written over the response.
func TestProblemOverRouterWriter_SeesCommitment(t *testing.T) {
	commits := []struct {
		name     string
		commit   func(t *testing.T, w http.ResponseWriter)
		wantCode int
		wantBody string
	}{
		{
			name: "final status",
			commit: func(_ *testing.T, w http.ResponseWriter) {
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte("first"))
			},
			wantCode: http.StatusAccepted,
			wantBody: "first",
		},
		{
			name: "hijack",
			commit: func(t *testing.T, w http.ResponseWriter) {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Fatalf("Hijack: %v", err)
				}
				_ = conn.Close()
			},
			wantCode: http.StatusOK,
		},
	}
	for _, entry := range problemEntries {
		for _, tt := range commits {
			t.Run(entry.name+"/"+tt.name, func(t *testing.T) {
				spy := newSpy()
				r := router.New()
				r.Get("/x", entry.route(spy, func(w http.ResponseWriter) { tt.commit(t, w) }))
				rec := httptest.NewRecorder()
				r.ServeHTTP(hijackRecorder{rec}, httptest.NewRequest(http.MethodGet, "/x", nil))

				if spy.calls != 1 || len(spy.ReportedErrors()) != 1 {
					t.Fatalf("calls = %d, reports = %d; want 1 and 1", spy.calls, len(spy.ReportedErrors()))
				}
				if !spy.rcWasW {
					t.Error("render context over the router writer: Written = false after the handler committed")
				}
				if rec.Code != tt.wantCode || rec.Body.String() != tt.wantBody {
					t.Errorf("response = %d %q, want %d %q", rec.Code, rec.Body.String(), tt.wantCode, tt.wantBody)
				}
			})
		}
	}
}

// TestProblemOverRouterWriter_EarlyHintsDoNotCommit asserts a 103 Early
// Hints alone does not count as committed for problem.ErrorHandler and
// problem.Middleware over the router's writer: the pipeline still answers
// the error, and the client receives its status, not 103.
func TestProblemOverRouterWriter_EarlyHintsDoNotCommit(t *testing.T) {
	for _, entry := range problemEntries {
		t.Run(entry.name, func(t *testing.T) {
			spy := newSpy()
			r := router.New()
			r.Get("/x", entry.route(spy, func(w http.ResponseWriter) {
				w.Header().Set("Link", "</app.css>; rel=preload; as=style")
				w.WriteHeader(http.StatusEarlyHints)
			}))
			srv := httptest.NewServer(r)
			defer srv.Close()

			resp, err := srv.Client().Get(srv.URL + "/x")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()

			if resp.StatusCode != http.StatusInternalServerError {
				t.Errorf("client status = %d, want 500", resp.StatusCode)
			}
			spy.mu.Lock()
			defer spy.mu.Unlock()
			if spy.calls != 1 || spy.rcWasW {
				t.Errorf("calls = %d, written before rendering = %v; want 1 and false", spy.calls, spy.rcWasW)
			}
		})
	}
}
