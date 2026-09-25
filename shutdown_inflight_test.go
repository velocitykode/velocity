package velocity

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/router"
)

// servingApp builds an app (debug off, logging to the returned logger)
// served by an httptest server whose requests hang off the app's shutdown
// context, as Serve wires them, with the server registered on the app so
// App.Shutdown drains it. finished receives, per finished request, whether
// the handler chain wrote anything.
func servingApp(t *testing.T) (a *App, srv *httptest.Server, logs *levelLogger, finished chan bool) {
	t.Helper()
	logs = &levelLogger{}
	const driverName = "shutdown-inflight-capture"
	prev := log.Drivers().Override(driverName, func(context.Context, log.LogConfig) (log.Logger, error) {
		return logs, nil
	})
	t.Cleanup(func() { log.Drivers().Override(driverName, prev) })

	a, err := New(WithConfig(Config{
		Env:   "testing",
		Port:  "0",
		Cache: CacheConfig{Driver: "memory", Prefix: "test_cache"},
		Log:   log.LogConfig{Driver: driverName, Config: make(map[string]any)},
		Queue: QueueConfig{Driver: "memory"},
		Mail:  mail.MailConfig{Driver: "log"},
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	finished = make(chan bool, 1)
	srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := &headerTracker{ResponseWriter: w}
		a.Router.ServeHTTP(tw, r)
		finished <- tw.wrote
	}))
	srv.Config.BaseContext = func(net.Listener) context.Context { return a.shutdownCtx }
	a.server = srv.Config
	srv.Start()
	t.Cleanup(srv.Close)
	return a, srv, logs, finished
}

// headerTracker records whether a status line was written through it.
type headerTracker struct {
	http.ResponseWriter
	wrote bool
}

func (h *headerTracker) WriteHeader(code int) {
	h.wrote = true
	h.ResponseWriter.WriteHeader(code)
}

func (h *headerTracker) Write(p []byte) (int, error) {
	h.wrote = true
	return h.ResponseWriter.Write(p)
}

// TestShutdown_StragglerPastDeadlineGets503 asserts a request still
// running when App.Shutdown's deadline ends is cut off through its context
// and answered 503 with Retry-After and Connection: close, logged at warn
// and not reported: not the empty 200 a client-gone cancel gets. The cut-off
// lands either in the handler or in outer middleware still running after a
// Timeout-wrapped handler finished in time (the context Timeout hands back
// must carry the shutdown cause).
func TestShutdown_StragglerPastDeadlineGets503(t *testing.T) {
	waitForCutOff := func(c *router.Context, entered chan struct{}) error {
		close(entered)
		<-c.Request.Context().Done()
		return c.Request.Context().Err()
	}
	tests := []struct {
		name     string
		register func(a *App, entered chan struct{})
	}{
		{
			name: "InHandler",
			register: func(a *App, entered chan struct{}) {
				a.Router.Get("/slow", func(c *router.Context) error { return waitForCutOff(c, entered) })
			},
		},
		{
			name: "InOuterMiddlewareAfterTimeout",
			register: func(a *App, entered chan struct{}) {
				a.Router.Use(func(next router.HandlerFunc) router.HandlerFunc {
					return func(c *router.Context) error {
						if err := next(c); err != nil {
							return err
						}
						return waitForCutOff(c, entered)
					}
				})
				a.Router.Use(router.Timeout(time.Minute))
				a.Router.Get("/slow", func(*router.Context) error { return nil })
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, srv, logs, finished := servingApp(t)
			entered := make(chan struct{})
			tt.register(a, entered)

			type result struct {
				resp *http.Response
				err  error
			}
			got := make(chan result, 1)
			go func() {
				resp, err := srv.Client().Get(srv.URL + "/slow")
				got <- result{resp, err}
			}()
			<-entered

			errorsBefore := logs.count("error")
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_ = a.Shutdown(ctx)

			res := <-got
			if res.err != nil {
				t.Fatalf("client: %v", res.err)
			}
			defer res.resp.Body.Close()
			_, _ = io.Copy(io.Discard, res.resp.Body)
			if res.resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", res.resp.StatusCode)
			}
			if got := res.resp.Header.Get("Retry-After"); got != "1" {
				t.Errorf("Retry-After = %q, want 1", got)
			}
			if !res.resp.Close {
				t.Error("response does not close the connection")
			}
			if !<-finished {
				t.Error("the handler chain wrote nothing")
			}
			if logs.count("warn") == 0 {
				t.Error("no warn line for the cut-off request")
			}
			if n := logs.count("error") - errorsBefore; n != 0 {
				t.Errorf("%d error lines (a report), want none", n)
			}
			if cause := context.Cause(a.shutdownCtx); !errors.Is(cause, contract.ErrServerShuttingDown) {
				t.Errorf("shutdown context cause = %v, want ErrServerShuttingDown", cause)
			}
		})
	}
}

// TestShutdown_DrainsBeforeCancelling asserts a request that finishes
// within App.Shutdown's deadline completes with its context still live and
// its own response, and the shutdown context is cancelled only after.
func TestShutdown_DrainsBeforeCancelling(t *testing.T) {
	a, srv, _, finished := servingApp(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	ctxErrAtEnd := make(chan error, 1)
	a.Router.Get("/work", func(c *router.Context) error {
		close(entered)
		<-release
		ctxErrAtEnd <- c.Request.Context().Err()
		return c.String(http.StatusCreated, "done")
	})

	got := make(chan *http.Response, 1)
	go func() {
		resp, err := srv.Client().Get(srv.URL + "/work")
		if err != nil {
			t.Errorf("client: %v", err)
		}
		got <- resp
	}()
	<-entered

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownDone <- a.Shutdown(ctx)
	}()
	time.Sleep(50 * time.Millisecond)
	if a.shutdownCtx.Err() != nil {
		t.Error("shutdown context cancelled before the in-flight request finished")
	}
	close(release)

	if err := <-ctxErrAtEnd; err != nil {
		t.Errorf("request context at completion = %v, want live", err)
	}
	resp := <-got
	if resp == nil {
		t.FailNow()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated || string(body) != "done" {
		t.Errorf("response = %d %q, want 201 done", resp.StatusCode, body)
	}
	<-finished
	if err := <-shutdownDone; err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	if !errors.Is(context.Cause(a.shutdownCtx), contract.ErrServerShuttingDown) {
		t.Errorf("shutdown context cause = %v, want ErrServerShuttingDown", context.Cause(a.shutdownCtx))
	}
}

// TestShutdown_ClientDisconnectWritesNothing asserts a request whose
// client went away (not the server) still gets nothing written and
// nothing logged.
func TestShutdown_ClientDisconnectWritesNothing(t *testing.T) {
	a, srv, logs, finished := servingApp(t)
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	entered := make(chan struct{})
	a.Router.Get("/wait", func(c *router.Context) error {
		close(entered)
		<-c.Request.Context().Done()
		return c.Request.Context().Err()
	})

	warnBefore, errorBefore := logs.count("warn"), logs.count("error")
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/wait", nil)
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		if resp, err := srv.Client().Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	<-entered
	cancel()
	<-clientDone

	select {
	case wrote := <-finished:
		if wrote {
			t.Error("a response was written for a client that went away")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish after the client went away")
	}
	if w, e := logs.count("warn")-warnBefore, logs.count("error")-errorBefore; w != 0 || e != 0 {
		t.Errorf("logged %d warn and %d error lines, want none", w, e)
	}
}
