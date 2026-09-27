package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/trace"
)

// lineRecorder collects the warn and error lines a levelLogger hands it.
type lineRecorder struct {
	mu     sync.Mutex
	warns  []string
	errors []string
}

func (l *lineRecorder) logger() levelLogger {
	return levelLogger{
		onWarn: func(msg string, kvs ...any) {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.warns = append(l.warns, msg)
		},
		onError: func(msg string, kvs ...any) {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.errors = append(l.errors, msg)
		},
	}
}

// count returns how many lines at level ("warn" or "error") contain part.
func (l *lineRecorder) count(level, part string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := l.warns
	if level == "error" {
		lines = l.errors
	}
	n := 0
	for _, line := range lines {
		if strings.Contains(line, part) {
			n++
		}
	}
	return n
}

// Registering a route, group, middleware or resource on a router that is
// already serving writes one warn line through the logger the router
// holds (SetLogger), for every registration entry point, and nothing
// through the standard library log or slog.Default.
func TestLateRegistration_WarnsThroughTheRouterLogger(t *testing.T) {
	stdlib := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)
	rec := &lineRecorder{}

	r := New()
	r.SetLogger(rec.logger())
	g := r.Group("/api")
	ok := func(c *Context) error { return nil }
	r.Get("/", ok)
	r.Freeze()

	cases := []struct {
		name     string
		register func()
	}{
		{"router route", func() { r.Get("/late", ok) }},
		{"router group", func() { r.Group("/late-group") }},
		{"router middleware", func() { r.Use(func(next HandlerFunc) HandlerFunc { return next }) }},
		{"router resource", func() { r.Resource("/late-things", struct{}{}) }},
		{"group route", func() { g.Get("/late", ok) }},
		{"group group", func() { g.Group("/late-group") }},
		{"group middleware", func() { g.Use(func(next HandlerFunc) HandlerFunc { return next }) }},
		{"group resource", func() { g.Resource("/late-things", struct{}{}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := rec.count("warn", "after server start")
			tc.register()
			if got := rec.count("warn", "after server start") - before; got != 1 {
				t.Errorf("router logger warn lines = %d, want 1", got)
			}
		})
	}
	if out := stdlib.String(); out != "" {
		t.Errorf("stdlib log / slog.Default got %q, want nothing", out)
	}
	if out := fallback.String(); out != "" {
		t.Errorf("fallback logger got %q, want nothing (the router has a logger)", out)
	}
}

// A router with no logger writes the late-registration warning through the
// framework's standalone fallback logger.
func TestLateRegistration_WithoutLoggerWarnsThroughTheFallback(t *testing.T) {
	stdlib := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)

	r := New()
	r.Freeze()
	r.Get("/late", func(c *Context) error { return nil })

	if got := fallback.Count("WARN", "velocity/router: route registered after server start"); got != 1 {
		t.Errorf("fallback warn lines = %d, want 1 (%q)", got, fallback.String())
	}
	if line := fallback.String(); !strings.Contains(line, "method=GET route=/late") {
		t.Errorf("line %q does not name the method and the route pattern", line)
	}
	if out := stdlib.String(); out != "" {
		t.Errorf("stdlib log / slog.Default got %q, want nothing", out)
	}
}

// CORS with every origin allowed and credentials on warns once, on the
// first request the middleware sees, through the logger of the router
// serving it; building the middleware writes nothing anywhere.
func TestCORS_WildcardWithCredentialsWarnsOnceThroughTheRouterLogger(t *testing.T) {
	stdlib := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)
	rec := &lineRecorder{}

	cfg := InsecureAllowAllCORS()
	cfg.AllowCredentials = true
	mw := CORS(cfg)
	if out := stdlib.String() + fallback.String(); out != "" {
		t.Fatalf("building the middleware wrote %q, want nothing", out)
	}

	r := New()
	r.SetLogger(rec.logger())
	r.Use(mw)
	r.Get("/", func(c *Context) error { return c.String(http.StatusOK, "ok") })
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Origin", "https://evil.example")
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	if got := rec.count("warn", "velocity/router: CORS allows every origin with credentials"); got != 1 {
		t.Errorf("router logger warn lines = %d, want 1", got)
	}
	if out := stdlib.String() + fallback.String(); out != "" {
		t.Errorf("stdlib log / slog.Default / fallback got %q, want nothing", out)
	}
}

// A safe CORS configuration never warns; the dangerous one on a router
// with no logger warns once through the fallback logger.
func TestCORS_WarningDestinations(t *testing.T) {
	serve := func(r *VelocityRouterV2) {
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Origin", "https://app.example")
			r.ServeHTTP(httptest.NewRecorder(), req)
		}
	}
	ok := func(c *Context) error { return c.String(http.StatusOK, "ok") }

	t.Run("explicit origins", func(t *testing.T) {
		fallback := fallbacklogtest.Capture(t)
		rec := &lineRecorder{}
		r := New()
		r.SetLogger(rec.logger())
		r.Use(CORS(CORSConfig{AllowedOrigins: []string{"https://app.example"}, AllowCredentials: true}))
		r.Get("/", ok)
		serve(r)
		if got := rec.count("warn", "CORS"); got != 0 {
			t.Errorf("warn lines = %d, want 0", got)
		}
		if out := fallback.String(); out != "" {
			t.Errorf("fallback got %q, want nothing", out)
		}
	})

	t.Run("no logger", func(t *testing.T) {
		fallback := fallbacklogtest.Capture(t)
		cfg := InsecureAllowAllCORS()
		cfg.AllowCredentials = true
		r := New()
		r.Use(CORS(cfg))
		r.Get("/", ok)
		serve(r)
		if got := fallback.Count("WARN", "velocity/router: CORS allows every origin with credentials"); got != 1 {
			t.Errorf("fallback warn lines = %d, want 1 (%q)", got, fallback.String())
		}
	})
}

// RateLimitByIP's private-peer advisory goes through the logger of the
// router serving the request, once per middleware.
func TestRateLimitByIP_PrivatePeerWarnsThroughTheRouterLogger(t *testing.T) {
	stdlib := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)
	rec := &lineRecorder{}

	r := New()
	r.SetLogger(rec.logger())
	r.Use(RateLimitByIP(100, time.Minute))
	r.Get("/", func(c *Context) error { return c.String(http.StatusOK, "ok") })
	for _, peer := range []string{"10.0.0.1:443", "192.168.1.5:443"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = peer
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	if got := rec.count("warn", "private or loopback peer"); got != 1 {
		t.Errorf("router logger warn lines = %d, want 1", got)
	}
	if out := stdlib.String() + fallback.String(); out != "" {
		t.Errorf("stdlib log / slog.Default / fallback got %q, want nothing", out)
	}
}

// panickingReporter is an error handler whose Report panics.
type panickingReporter struct{ contract.ErrorHandler }

func (panickingReporter) Report(error, *contract.ErrorContext) { panic("reporter broke") }

// A late Timeout panic on a request no router dispatched and with no error
// handler is written through the services' logger, else the fallback; a
// failure while reporting it is written the same way. Nothing goes through
// the standard library log.
func TestReportLatePanic_WritesThroughTheServicesLoggerOrTheFallback(t *testing.T) {
	latePanic := func() error {
		return newPanicError(errors.New("late boom"), 0)
	}

	t.Run("services logger", func(t *testing.T) {
		stdlib := fallbacklogtest.CaptureStdlib(t)
		fallback := fallbacklogtest.Capture(t)
		rec := &lineRecorder{}
		c, _ := NewTestContext(http.MethodGet, "/")
		c.services = &app.Services{Log: rec.logger()}
		reportLatePanic(c, latePanic())
		if got := rec.count("error", "velocity/router: timeout handler panicked after the timeout answered"); got != 1 {
			t.Errorf("services logger error lines = %d, want 1", got)
		}
		if out := stdlib.String() + fallback.String(); out != "" {
			t.Errorf("stdlib / fallback got %q, want nothing", out)
		}
	})

	t.Run("no logger", func(t *testing.T) {
		stdlib := fallbacklogtest.CaptureStdlib(t)
		fallback := fallbacklogtest.Capture(t)
		c, _ := NewTestContext(http.MethodGet, "/")
		reportLatePanic(c, latePanic())
		if got := fallback.Count("ERROR", "velocity/router: timeout handler panicked after the timeout answered"); got != 1 {
			t.Errorf("fallback error lines = %d, want 1 (%q)", got, fallback.String())
		}
		if out := stdlib.String(); out != "" {
			t.Errorf("stdlib got %q, want nothing", out)
		}
	})

	t.Run("reporting fails", func(t *testing.T) {
		stdlib := fallbacklogtest.CaptureStdlib(t)
		rec := &lineRecorder{}
		c, _ := NewTestContext(http.MethodGet, "/")
		c.services = &app.Services{Log: rec.logger(), Errors: panickingReporter{}}
		reportLatePanic(c, latePanic())
		if got := rec.count("error", "velocity/router: reporting a timeout handler panic after the timeout failed"); got != 1 {
			t.Errorf("services logger error lines = %d, want 1", got)
		}
		if out := stdlib.String(); out != "" {
			t.Errorf("stdlib got %q, want nothing", out)
		}
	})
}

// fieldLine is one line a fieldRecorder saw: its message and key-values.
type fieldLine struct {
	msg string
	kvs map[string]any
}

// fieldRecorder records every warn and error line with its key-values,
// the pairs a With binding added included.
type fieldRecorder struct {
	mu    sync.Mutex
	lines []fieldLine
}

func (f *fieldRecorder) logger() levelLogger {
	record := func(msg string, kvs ...any) {
		m := map[string]any{}
		for i := 0; i+1 < len(kvs); i += 2 {
			if k, ok := kvs[i].(string); ok {
				m[k] = kvs[i+1]
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lines = append(f.lines, fieldLine{msg: msg, kvs: m})
	}
	return levelLogger{onWarn: record, onError: record}
}

// line returns the one recorded line containing part.
func (f *fieldRecorder) line(t *testing.T, part string) fieldLine {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var found []fieldLine
	for _, l := range f.lines {
		if strings.Contains(l.msg, part) {
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		t.Fatalf("lines containing %q = %d, want 1 (%v)", part, len(found), f.lines)
	}
	return found[0]
}

// The CORS, rate-limit and late Timeout panic lines carry the request's
// ids the way c.Log() lines do: request_id, trace_id and span_id equal to
// the ids the handler's request context holds, plus the method.
func TestRequestLines_CarryTheRequestFields(t *testing.T) {
	type ids struct{ requestID, traceID, spanID string }
	check := func(t *testing.T, l fieldLine, want ids) {
		t.Helper()
		for key, v := range map[string]string{"request_id": want.requestID, "trace_id": want.traceID, "span_id": want.spanID, "method": http.MethodGet} {
			if v == "" {
				t.Fatalf("want %s is empty; the request carried none", key)
			}
			if got := l.kvs[key]; got != v {
				t.Errorf("line %q %s = %v, want %q", l.msg, key, got, v)
			}
		}
	}
	serve := func(t *testing.T, mw MiddlewareFunc, peer string, logger contract.Logger) ids {
		t.Helper()
		var seen ids
		r := New()
		r.SetLogger(logger)
		r.Use(mw)
		r.Get("/", func(c *Context) error {
			ctx := c.Request.Context()
			seen = ids{trace.GetRequestID(ctx), trace.GetTraceID(ctx), trace.GetSpanID(ctx)}
			return c.String(http.StatusOK, "ok")
		})
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Origin", "https://app.example")
		req.RemoteAddr = peer
		r.ServeHTTP(httptest.NewRecorder(), req)
		return seen
	}

	t.Run("cors", func(t *testing.T) {
		rec := &fieldRecorder{}
		cfg := InsecureAllowAllCORS()
		cfg.AllowCredentials = true
		want := serve(t, CORS(cfg), "203.0.113.9:443", rec.logger())
		check(t, rec.line(t, "CORS allows every origin"), want)
	})

	t.Run("rate limit", func(t *testing.T) {
		rec := &fieldRecorder{}
		want := serve(t, RateLimitByIP(100, time.Minute), "10.0.0.1:443", rec.logger())
		l := rec.line(t, "private or loopback peer")
		check(t, l, want)
		if l.kvs["ip"] != "10.0.0.1" {
			t.Errorf("ip = %v, want 10.0.0.1", l.kvs["ip"])
		}
	})

	t.Run("timeout late panic", func(t *testing.T) {
		rec := &fieldRecorder{}
		c, _ := NewTestContext(http.MethodGet, "/")
		want := ids{"req-late-1", "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"}
		ctx := trace.WithTrace(trace.WithRequestID(c.Request.Context(), want.requestID), want.traceID, want.spanID)
		c.Request = c.Request.WithContext(ctx)
		c.services = &app.Services{Log: rec.logger()}
		reportLatePanic(c, newPanicError(errors.New("late boom"), 0))
		check(t, rec.line(t, "timeout handler panicked after the timeout answered"), want)
	})

	t.Run("timeout reporting fails", func(t *testing.T) {
		rec := &fieldRecorder{}
		c, _ := NewTestContext(http.MethodGet, "/")
		want := ids{"req-late-2", "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"}
		ctx := trace.WithTrace(trace.WithRequestID(c.Request.Context(), want.requestID), want.traceID, want.spanID)
		c.Request = c.Request.WithContext(ctx)
		c.services = &app.Services{Log: rec.logger(), Errors: panickingReporter{}}
		reportLatePanic(c, newPanicError(errors.New("late boom"), 0))
		check(t, rec.line(t, "reporting a timeout handler panic after the timeout failed"), want)
	})
}

// requestLogger is nil-safe: a nil Context, and a Context with no request,
// no services and no router, both resolve to the fallback, and a line
// through them writes there without panicking.
func TestRequestLogger_NilAndZeroContext(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	requestLogger(nil).Warn("velocity/router: nil context line")
	requestLogger(&Context{}).Warn("velocity/router: zero context line")
	if got := fallback.Count("WARN", "velocity/router: nil context line"); got != 1 {
		t.Errorf("nil context fallback lines = %d, want 1 (%q)", got, fallback.String())
	}
	if got := fallback.Count("WARN", "velocity/router: zero context line"); got != 1 {
		t.Errorf("zero context fallback lines = %d, want 1 (%q)", got, fallback.String())
	}
}

// A dropped flash warns through the services' logger with the request's
// fields, and through the fallback when the Context has no logger at all
// (it used to be dropped without a line).
func TestFlashDropped_WarnsThroughTheRequestLogger(t *testing.T) {
	t.Run("services logger", func(t *testing.T) {
		fallback := fallbacklogtest.Capture(t)
		rec := &fieldRecorder{}
		c, _ := NewTestContext(http.MethodGet, "/")
		c.Request = c.Request.WithContext(trace.WithRequestID(c.Request.Context(), "req-flash-1"))
		c.services = &app.Services{Log: rec.logger()}
		c.FlashErrors(map[string]string{"email": "required"})
		l := rec.line(t, "flash dropped: the request carries no session")
		if l.kvs["request_id"] != "req-flash-1" || l.kvs["key"] != FlashErrorsKey {
			t.Errorf("line kvs = %v, want request_id req-flash-1 and key %q", l.kvs, FlashErrorsKey)
		}
		if out := fallback.String(); out != "" {
			t.Errorf("fallback got %q, want nothing", out)
		}
	})

	t.Run("no logger", func(t *testing.T) {
		fallback := fallbacklogtest.Capture(t)
		c, _ := NewTestContext(http.MethodGet, "/")
		c.FlashInput(map[string]string{"name": "x"})
		if got := fallback.Count("WARN", "velocity/router: flash dropped: the request carries no session"); got != 1 {
			t.Errorf("fallback warn lines = %d, want 1 (%q)", got, fallback.String())
		}
	})
}
