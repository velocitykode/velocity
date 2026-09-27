package httpclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/httpclient"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/trace"
)

var traceparentShape = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-01$`)

// upstreamRecorder is an httptest server that records the propagation
// headers of every request it receives.
type upstreamRecorder struct {
	mu          sync.Mutex
	traceparent []string
	requestID   []string
}

func (u *upstreamRecorder) handler(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.traceparent = append(u.traceparent, r.Header.Get("traceparent"))
	u.requestID = append(u.requestID, r.Header.Get("X-Request-ID"))
	u.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (u *upstreamRecorder) last() (traceparent, requestID string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.traceparent) == 0 {
		return "", ""
	}
	return u.traceparent[len(u.traceparent)-1], u.requestID[len(u.requestID)-1]
}

type eventLog struct {
	mu     sync.Mutex
	events []any
}

func (l *eventLog) dispatch(_ context.Context, ev any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
	return nil
}

func (l *eventLog) snapshot() []any {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]any(nil), l.events...)
}

// TestClient_DoInsideHandlerCarriesTraceparent drives an outbound call from
// a real router handler: the upstream sees a traceparent in the handler's
// trace whose parent-id is the outbound call's own span, that span is a
// child of the request span, and the request id travels as X-Request-ID.
func TestClient_DoInsideHandlerCarriesTraceparent(t *testing.T) {
	up := &upstreamRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(up.handler))
	defer srv.Close()

	events := &eventLog{}
	client := httpclient.New(httpclient.WithoutPrivateIPDeny())
	client.SetEventDispatcher(events.dispatch)

	r := router.NewV2()
	r.SetEventDispatcher(events.dispatch)
	r.Get("/call", func(c *router.Context) error {
		resp, err := client.Get(c.Request.Context(), srv.URL)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		return c.String(http.StatusOK, "ok")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/call", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("handler status = %d, want 200", rec.Code)
	}

	var handled *router.RequestHandled
	var sent *httpclient.RequestSent
	for _, ev := range events.snapshot() {
		switch e := ev.(type) {
		case *router.RequestHandled:
			handled = e
		case *httpclient.RequestSent:
			sent = e
		}
	}
	if handled == nil || sent == nil {
		t.Fatalf("missing events: RequestHandled=%v RequestSent=%v", handled != nil, sent != nil)
	}

	header, requestID := up.last()
	m := traceparentShape.FindStringSubmatch(header)
	if m == nil {
		t.Fatalf("upstream traceparent = %q, want 00-<trace>-<span>-01", header)
	}
	if m[1] != handled.TraceID {
		t.Errorf("traceparent trace id = %q, want the handler's %q", m[1], handled.TraceID)
	}
	if m[2] != sent.SpanID {
		t.Errorf("traceparent parent id = %q, want the outbound call's span %q", m[2], sent.SpanID)
	}
	if sent.TraceID != handled.TraceID {
		t.Errorf("RequestSent.TraceID = %q, want %q", sent.TraceID, handled.TraceID)
	}
	if sent.SpanID == "" || sent.SpanID == handled.SpanID {
		t.Errorf("RequestSent.SpanID = %q, want its own span (request span %q)", sent.SpanID, handled.SpanID)
	}
	if sent.ParentID != handled.SpanID {
		t.Errorf("RequestSent.ParentID = %q, want the request span %q", sent.ParentID, handled.SpanID)
	}
	if requestID == "" || requestID != handled.RequestID {
		t.Errorf("upstream X-Request-ID = %q, want the handler's request id %q", requestID, handled.RequestID)
	}
}

// TestClient_DoWithoutTraceStartsRoot covers an outbound call made with no
// trace in its context (a CLI command, a background goroutine): the call is
// a root span, so the upstream still receives a well-formed traceparent and
// the event records no parent.
func TestClient_DoWithoutTraceStartsRoot(t *testing.T) {
	up := &upstreamRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(up.handler))
	defer srv.Close()

	events := &eventLog{}
	client := httpclient.New(httpclient.WithoutPrivateIPDeny())
	client.SetEventDispatcher(events.dispatch)

	resp, err := client.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = resp.Body.Close()

	var sent *httpclient.RequestSent
	for _, ev := range events.snapshot() {
		if e, ok := ev.(*httpclient.RequestSent); ok {
			sent = e
		}
	}
	if sent == nil {
		t.Fatal("RequestSent not dispatched")
	}
	header, requestID := up.last()
	m := traceparentShape.FindStringSubmatch(header)
	if m == nil {
		t.Fatalf("upstream traceparent = %q, want 00-<trace>-<span>-01", header)
	}
	if m[1] != sent.TraceID || m[2] != sent.SpanID {
		t.Errorf("traceparent = %q, want trace %q span %q", header, sent.TraceID, sent.SpanID)
	}
	if sent.ParentID != "" {
		t.Errorf("RequestSent.ParentID = %q, want empty for a root span", sent.ParentID)
	}
	if requestID != "" {
		t.Errorf("upstream X-Request-ID = %q, want none (the context carries no request id)", requestID)
	}
}

// TestClient_DoKeepsCallerHeaders pins two guarantees: a traceparent or
// X-Request-ID the caller set on the request wins over the context's, and
// the propagation headers are written on the client's copy, never on the
// caller's *http.Request.
func TestClient_DoKeepsCallerHeaders(t *testing.T) {
	up := &upstreamRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(up.handler))
	defer srv.Close()

	client := httpclient.New(httpclient.WithoutPrivateIPDeny())
	ctx := trace.WithTrace(context.Background(), "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7")

	own := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("traceparent", own)
	req.Header.Set("X-Request-ID", "caller-chosen")
	resp, err := client.Do(ctx, req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	if header, requestID := up.last(); header != own || requestID != "caller-chosen" {
		t.Errorf("upstream saw traceparent %q and X-Request-ID %q, want the caller's %q and %q", header, requestID, own, "caller-chosen")
	}

	bare, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = client.Do(ctx, bare)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_ = resp.Body.Close()
	if got := bare.Header.Get("traceparent"); got != "" {
		t.Errorf("caller's request was mutated: traceparent = %q", got)
	}
	if header, _ := up.last(); traceparentShape.FindStringSubmatch(header) == nil {
		t.Errorf("upstream traceparent = %q, want one written by the client", header)
	}
}
