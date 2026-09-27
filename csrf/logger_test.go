package csrf

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/csrf/stores"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	logdrivers "github.com/velocitykode/velocity/log/drivers"
	"github.com/velocitykode/velocity/trace"
)

var (
	_ contract.LoggerAware = (*CSRF)(nil)
	_ contract.LoggerAware = (*stores.SessionBagStore)(nil)
)

// rawSessionID is the session id every test below binds its token to; no
// log line may contain it.
const rawSessionID = "raw-session-id-4f1c9e"

// errStoreDown is the failure the failing stores return.
var errStoreDown = errors.New("token store unreachable")

// failingDeleteStore holds tokens but fails every Delete.
type failingDeleteStore struct{ *nonAtomicStore }

func (failingDeleteStore) Delete(context.Context, string) error { return errStoreDown }

// failingConsumeStore holds tokens but fails every ConsumeIfMatch.
type failingConsumeStore struct{ *nonAtomicStore }

func (failingConsumeStore) ConsumeIfMatch(context.Context, string, string) (bool, error) {
	return false, errStoreDown
}

func (failingConsumeStore) ConsumptionScope() stores.ConsumptionScope {
	return stores.ConsumedEverywhere
}

// postWithSessionToken runs one unsafe request carrying token for rawSessionID
// through c's middleware and returns the status.
func postWithSessionToken(c *CSRF, token string) int {
	req := httptest.NewRequest(http.MethodPost, "/submit", nil)
	req.Header.Set("X-CSRF-Token", token)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: rawSessionID})
	w := httptest.NewRecorder()
	c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(w, req)
	return w.Code
}

// singleUseCSRF returns a single-use CSRF instance over store, with a
// token seeded for rawSessionID.
func singleUseCSRF(t *testing.T, store Store) (*CSRF, string) {
	t.Helper()
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if err := store.Set(context.Background(), rawSessionID, token); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cfg := DefaultConfig()
	cfg.SessionIDResolver = testCookieResolver("session_id")
	cfg.SingleUse = true
	cfg.Store = store
	return New(cfg), token
}

// The three token-store failures the CSRF instance logs (consuming a
// single-use token, deleting one, deleting the old session's token on
// rotation) each write one error line through the instance's logger, and
// none of them names the session id. Nothing goes through the standard
// library log or the fallback logger.
func TestCSRF_StoreFailuresLogThroughItsLoggerWithoutTheSessionID(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		run  func(t *testing.T, c func(Store) (*CSRF, string))
	}{
		{
			name: "consume single-use token",
			msg:  "velocity/csrf: consume single-use token failed",
			run: func(t *testing.T, build func(Store) (*CSRF, string)) {
				c, token := build(failingConsumeStore{newNonAtomicStore()})
				if code := postWithSessionToken(c, token); code == http.StatusOK {
					t.Fatalf("status = %d, want a rejection", code)
				}
			},
		},
		{
			name: "delete single-use token",
			msg:  "velocity/csrf: delete single-use token failed",
			run: func(t *testing.T, build func(Store) (*CSRF, string)) {
				c, token := build(failingDeleteStore{newNonAtomicStore()})
				if code := postWithSessionToken(c, token); code != http.StatusOK {
					t.Fatalf("status = %d, want 200", code)
				}
			},
		},
		{
			name: "rotate token",
			msg:  "velocity/csrf: rotate token: delete the old session's token failed",
			run: func(t *testing.T, build func(Store) (*CSRF, string)) {
				c, _ := build(failingDeleteStore{newNonAtomicStore()})
				if err := c.RotateToken(context.Background(), rawSessionID, "new-session-id"); err != nil {
					t.Fatalf("RotateToken: %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdlib := fallbacklogtest.CaptureStdlib(t)
			fallback := fallbacklogtest.Capture(t)
			out := &fallbacklogtest.Output{}
			build := func(store Store) (*CSRF, string) {
				c, token := singleUseCSRF(t, store)
				c.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
				return c, token
			}
			tc.run(t, build)

			if got := strings.Count(out.String(), "ERROR: "+tc.msg); got != 1 {
				t.Errorf("logger error lines = %d, want 1 (%q)", got, out.String())
			}
			if !strings.Contains(out.String(), errStoreDown.Error()) {
				t.Errorf("error line does not carry the store error: %q", out.String())
			}
			for name, s := range map[string]string{"logger": out.String(), "fallback": fallback.String(), "stdlib": stdlib.String()} {
				if strings.Contains(s, rawSessionID) {
					t.Errorf("%s output contains the raw session id: %q", name, s)
				}
			}
			if s := stdlib.String() + fallback.String(); s != "" {
				t.Errorf("stdlib / fallback got %q, want nothing", s)
			}
		})
	}
}

// Without a logger, the same failures go through the fallback logger, still
// without the session id.
func TestCSRF_StoreFailureWithoutLoggerUsesTheFallback(t *testing.T) {
	stdlib := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)
	c, token := singleUseCSRF(t, failingDeleteStore{newNonAtomicStore()})
	if code := postWithSessionToken(c, token); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := fallback.Count("ERROR", "velocity/csrf: delete single-use token failed"); got != 1 {
		t.Errorf("fallback error lines = %d, want 1 (%q)", got, fallback.String())
	}
	if strings.Contains(fallback.String(), rawSessionID) {
		t.Errorf("fallback line contains the raw session id: %q", fallback.String())
	}
	if s := stdlib.String(); s != "" {
		t.Errorf("stdlib got %q, want nothing", s)
	}
}

// SetLogger hands the logger to a store that takes one: the session-bag
// store's outside-the-session warning goes through the CSRF instance's
// logger.
func TestCSRF_SetLoggerReachesTheSessionBagStore(t *testing.T) {
	stdlib := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)
	out := &fallbacklogtest.Output{}
	store := stores.NewSessionBagStore(func(context.Context) stores.SessionBag { return nil }, 0)
	cfg := DefaultConfig()
	cfg.SessionIDResolver = testCookieResolver("session_id")
	cfg.Store = store
	c := New(cfg)
	c.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))

	// A safe request asks the store for the session's token to issue it.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/form", nil)
		req.AddCookie(&http.Cookie{Name: "session_id", Value: rawSessionID})
		c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(httptest.NewRecorder(), req)
	}

	if got := strings.Count(out.String(), "WARN: velocity/csrf: the CSRF token lives in the session"); got != 1 {
		t.Errorf("logger warn lines = %d, want 1 (%q)", got, out.String())
	}
	if s := stdlib.String() + fallback.String(); s != "" {
		t.Errorf("stdlib / fallback got %q, want nothing", s)
	}
}

// SetLogger may run while requests that write through the logger are
// served: the CSRF instance and the session-bag store hold it under a
// read-write mutex.
func TestCSRF_SetLoggerWhileServingIsSafe(t *testing.T) {
	fallbacklogtest.Capture(t)
	out := &fallbacklogtest.Output{}
	c, token := singleUseCSRF(t, failingDeleteStore{newNonAtomicStore()})
	// Each bag writes its warning once, so each is read once: use many.
	bags := make([]*stores.SessionBagStore, 50)
	for i := range bags {
		bags[i] = stores.NewSessionBagStore(func(context.Context) stores.SessionBag { return nil }, 0)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				l := logdrivers.NewConsoleLoggerTo(out, 0)
				c.SetLogger(l)
				bags[j].SetLogger(l)
				c.SetLogger(nil)
				bags[j].SetLogger(nil)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = c.config.Store.Set(context.Background(), rawSessionID, token)
				_ = postWithSessionToken(c, token)
				_, _ = bags[j].Get(context.Background(), rawSessionID)
			}
		}()
	}
	wg.Wait()
}

// requestIDs is the request, trace and span id set every request-bound
// line below must carry.
const requestIDs = "request_id=req-csrf-1 trace_id=4bf92f3577b34da6a3ce929d0e0e4736 span_id=00f067aa0ba902b7"

// withRequestIDs returns ctx carrying the ids requestIDs names.
func withRequestIDs(ctx context.Context) context.Context {
	return trace.WithTrace(trace.WithRequestID(ctx, "req-csrf-1"), "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7")
}

// Every line the CSRF instance and its session-bag store write while
// serving a request carries the request's ids: the three store failures,
// the single-use scope warning and the outside-the-session warning.
func TestCSRF_LinesCarryTheRequestIDs(t *testing.T) {
	post := func(c *CSRF, token string) {
		req := httptest.NewRequest(http.MethodPost, "/submit", nil)
		req = req.WithContext(withRequestIDs(req.Context()))
		req.Header.Set("X-CSRF-Token", token)
		req.AddCookie(&http.Cookie{Name: "session_id", Value: rawSessionID})
		c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(httptest.NewRecorder(), req)
	}
	cases := []struct {
		name  string
		lines []string
		run   func(out *fallbacklogtest.Output)
	}{
		{"consume single-use token", []string{"ERROR: velocity/csrf: consume single-use token failed"}, func(out *fallbacklogtest.Output) {
			c, token := singleUseCSRF(t, failingConsumeStore{newNonAtomicStore()})
			c.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
			post(c, token)
		}},
		{"delete single-use token", []string{"WARN: velocity/csrf: SingleUse is exact per process only", "ERROR: velocity/csrf: delete single-use token failed"}, func(out *fallbacklogtest.Output) {
			c, token := singleUseCSRF(t, failingDeleteStore{newNonAtomicStore()})
			c.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
			post(c, token)
		}},
		{"rotate token", []string{"ERROR: velocity/csrf: rotate token"}, func(out *fallbacklogtest.Output) {
			c, _ := singleUseCSRF(t, failingDeleteStore{newNonAtomicStore()})
			c.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
			_ = c.RotateToken(withRequestIDs(context.Background()), rawSessionID, "new-session-id")
		}},
		{"outside the session", []string{"WARN: velocity/csrf: the CSRF token lives in the session"}, func(out *fallbacklogtest.Output) {
			store := stores.NewSessionBagStore(func(context.Context) stores.SessionBag { return nil }, 0)
			store.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
			_, _ = store.Get(withRequestIDs(context.Background()), rawSessionID)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fallbacklogtest.Capture(t)
			out := &fallbacklogtest.Output{}
			tc.run(out)
			for _, want := range tc.lines {
				var line string
				for _, l := range out.Lines() {
					if strings.Contains(l, want) {
						line = l
					}
				}
				if line == "" {
					t.Fatalf("no line %q in %q", want, out.String())
				}
				if !strings.Contains(line, requestIDs) {
					t.Errorf("line %q does not carry %q", line, requestIDs)
				}
			}
		})
	}
}

// SetLogger's edge inputs: nil puts the instance and its store back on the
// fallback; a zero-value CSRF (no config, no store) takes a logger without
// panicking; a logger set after Shutdown still receives the lines.
func TestCSRF_SetLoggerEdgeInputs(t *testing.T) {
	t.Run("nil restores the fallback", func(t *testing.T) {
		fallback := fallbacklogtest.Capture(t)
		out := &fallbacklogtest.Output{}
		c, token := singleUseCSRF(t, failingDeleteStore{newNonAtomicStore()})
		c.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
		c.SetLogger(nil)
		_ = postWithSessionToken(c, token)
		if out.String() != "" {
			t.Errorf("replaced logger got %q, want nothing", out.String())
		}
		if got := fallback.Count("ERROR", "velocity/csrf: delete single-use token failed"); got != 1 {
			t.Errorf("fallback error lines = %d, want 1 (%q)", got, fallback.String())
		}
	})

	t.Run("zero value", func(t *testing.T) {
		fallbacklogtest.Capture(t)
		var c CSRF
		c.SetLogger(logdrivers.NewConsoleLoggerTo(&fallbacklogtest.Output{}, 0))
		c.SetLogger(nil)
		if err := c.RotateToken(context.Background(), "a", "b"); !errors.Is(err, ErrNoStore) {
			t.Errorf("RotateToken on a zero value = %v, want ErrNoStore", err)
		}
	})

	t.Run("after shutdown", func(t *testing.T) {
		fallback := fallbacklogtest.Capture(t)
		out := &fallbacklogtest.Output{}
		c, _ := singleUseCSRF(t, failingDeleteStore{newNonAtomicStore()})
		if err := c.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		c.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
		_ = c.RotateToken(context.Background(), rawSessionID, "new-session-id")
		if got := strings.Count(out.String(), "ERROR: velocity/csrf: rotate token"); got != 1 {
			t.Errorf("logger error lines = %d, want 1 (%q)", got, out.String())
		}
		if s := fallback.String(); s != "" {
			t.Errorf("fallback got %q, want nothing", s)
		}
	})
}

// A zero-value SessionBagStore takes a logger and, having no session
// lookup, warns through it (or the fallback after SetLogger(nil)).
func TestSessionBagStore_SetLoggerEdgeInputs(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	out := &fallbacklogtest.Output{}
	var withLogger, withNil stores.SessionBagStore
	withLogger.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
	withNil.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
	withNil.SetLogger(nil)
	if _, err := withLogger.Get(context.Background(), "id"); err == nil {
		t.Fatal("Get on a zero value succeeded, want an error")
	}
	_, _ = withNil.Get(context.Background(), "id")
	if got := strings.Count(out.String(), "WARN: velocity/csrf: the CSRF token lives in the session"); got != 1 {
		t.Errorf("logger warn lines = %d, want 1 (%q)", got, out.String())
	}
	if got := fallback.Count("WARN", "velocity/csrf: the CSRF token lives in the session"); got != 1 {
		t.Errorf("fallback warn lines = %d, want 1 (%q)", got, fallback.String())
	}
}

// loggerStore is a token store that records the logger it is handed.
type loggerStore struct {
	*nonAtomicStore
	mu     sync.Mutex
	logger contract.Logger
}

func (s *loggerStore) SetLogger(l contract.Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = l
}

// Concurrent SetLogger calls leave the instance and its store on the same
// logger: the store is handed it under the instance's lock.
func TestCSRF_ConcurrentSetLoggerKeepsTheStoreInStep(t *testing.T) {
	for round := 0; round < 50; round++ {
		store := &loggerStore{nonAtomicStore: newNonAtomicStore()}
		cfg := DefaultConfig()
		cfg.SessionIDResolver = testCookieResolver("session_id")
		cfg.Store = store
		c := New(cfg)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c.SetLogger(logdrivers.NewConsoleLoggerTo(&fallbacklogtest.Output{}, 0))
			}()
		}
		wg.Wait()
		c.logMu.RLock()
		want := c.logger
		c.logMu.RUnlock()
		store.mu.Lock()
		got := store.logger
		store.mu.Unlock()
		if got != want {
			t.Fatalf("round %d: store holds %p, the instance %p", round, got, want)
		}
	}
}
