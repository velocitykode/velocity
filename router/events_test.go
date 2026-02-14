package router

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// testEventCollector collects dispatched events for testing
type testEventCollector struct {
	mu     sync.Mutex
	events []interface{}
}

func newTestEventCollector() *testEventCollector {
	return &testEventCollector{
		events: make([]interface{}, 0),
	}
}

func (c *testEventCollector) dispatch(event interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return nil
}

func (c *testEventCollector) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = make([]interface{}, 0)
}

func (c *testEventCollector) getEvents() []interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]interface{}, len(c.events))
	copy(result, c.events)
	return result
}

func (c *testEventCollector) findEvent(predicate func(interface{}) bool) interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if predicate(e) {
			return e
		}
	}
	return nil
}

func TestRequestEventsDispatch(t *testing.T) {
	collector := newTestEventCollector()

	router := NewV2()
	router.SetInstanceEventDispatcher(collector.dispatch)
	router.Get("/users/{id}", func(c *Context) error {
		return c.JSON(http.StatusOK, map[string]string{"id": c.Param("id")})
	})

	req := httptest.NewRequest(http.MethodGet, "/users/123", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	// Verify RequestStarted was dispatched
	started := collector.findEvent(func(e interface{}) bool {
		if rs, ok := e.(*RequestStarted); ok {
			return rs.Method == "GET" && rs.Path == "/users/123"
		}
		return false
	})
	if started == nil {
		t.Error("RequestStarted not dispatched correctly")
	}

	// Verify RequestRouted was dispatched
	routed := collector.findEvent(func(e interface{}) bool {
		if rr, ok := e.(*RequestRouted); ok {
			return rr.Matched == true && rr.Route == "/users/{id}"
		}
		return false
	})
	if routed == nil {
		t.Error("RequestRouted not dispatched correctly")
	}

	// Verify RequestHandled was dispatched
	handled := collector.findEvent(func(e interface{}) bool {
		if rh, ok := e.(*RequestHandled); ok {
			return rh.Method == "GET" && rh.Path == "/users/123" && rh.StatusCode == http.StatusOK
		}
		return false
	})
	if handled == nil {
		t.Error("RequestHandled not dispatched correctly")
	}
}

func TestRequestEvents404(t *testing.T) {
	collector := newTestEventCollector()

	router := NewV2()
	router.SetInstanceEventDispatcher(collector.dispatch)
	router.Get("/users", func(c *Context) error {
		return c.JSON(http.StatusOK, nil)
	})

	req := httptest.NewRequest(http.MethodGet, "/notfound", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	// Verify RequestRouted was dispatched with Matched=false
	routed := collector.findEvent(func(e interface{}) bool {
		if rr, ok := e.(*RequestRouted); ok {
			return rr.Matched == false
		}
		return false
	})
	if routed == nil {
		t.Error("RequestRouted not dispatched correctly for 404")
	}

	// Verify RequestHandled was dispatched with 404 status
	handled := collector.findEvent(func(e interface{}) bool {
		if rh, ok := e.(*RequestHandled); ok {
			return rh.StatusCode == http.StatusNotFound
		}
		return false
	})
	if handled == nil {
		t.Error("RequestHandled not dispatched correctly for 404")
	}
}

func TestRequestEventsPanic(t *testing.T) {
	collector := newTestEventCollector()

	router := NewV2()
	router.SetInstanceEventDispatcher(collector.dispatch)
	router.Get("/panic", func(c *Context) error {
		panic("test panic")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	// Verify RequestFailed was dispatched
	failed := collector.findEvent(func(e interface{}) bool {
		if rf, ok := e.(*RequestFailed); ok {
			return rf.Recovered == true && rf.Stack != ""
		}
		return false
	})
	if failed == nil {
		t.Error("RequestFailed not dispatched correctly for panic")
	}

	// Verify response is 500
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", rec.Code)
	}
}

func TestRequestEventsHandlerError(t *testing.T) {
	collector := newTestEventCollector()

	router := NewV2()
	router.SetInstanceEventDispatcher(collector.dispatch)
	router.Get("/error", func(c *Context) error {
		return http.ErrAbortHandler
	})

	req := httptest.NewRequest(http.MethodGet, "/error", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	// Verify RequestFailed was dispatched
	failed := collector.findEvent(func(e interface{}) bool {
		if rf, ok := e.(*RequestFailed); ok {
			return rf.Error == http.ErrAbortHandler && rf.Recovered == false
		}
		return false
	})
	if failed == nil {
		t.Error("RequestFailed not dispatched correctly for handler error")
	}
}

func TestRequestEventsDuration(t *testing.T) {
	collector := newTestEventCollector()

	router := NewV2()
	router.SetInstanceEventDispatcher(collector.dispatch)
	router.Get("/slow", func(c *Context) error {
		time.Sleep(10 * time.Millisecond)
		return c.String(http.StatusOK, "done")
	})

	req := httptest.NewRequest(http.MethodGet, "/slow", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	// Verify duration was captured
	handled := collector.findEvent(func(e interface{}) bool {
		if rh, ok := e.(*RequestHandled); ok {
			return rh.Duration >= 10*time.Millisecond
		}
		return false
	})
	if handled == nil {
		t.Error("RequestHandled duration not captured correctly")
	}
}

func TestRequestEventsRequestID(t *testing.T) {
	collector := newTestEventCollector()

	router := NewV2()
	router.SetInstanceEventDispatcher(collector.dispatch)
	var capturedRequestID string
	router.Get("/test", func(c *Context) error {
		capturedRequestID = GetRequestID(c.Request)
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	// Verify request ID was generated and available
	if capturedRequestID == "" {
		t.Error("Request ID was not set in context")
	}

	// Verify all events have same request ID
	events := collector.getEvents()
	var startedID, routedID, handledID string
	for _, event := range events {
		switch e := event.(type) {
		case *RequestStarted:
			startedID = e.RequestID
		case *RequestRouted:
			routedID = e.RequestID
		case *RequestHandled:
			handledID = e.RequestID
		}
	}

	if startedID != routedID || routedID != handledID {
		t.Errorf("Request IDs do not match across events: started=%s, routed=%s, handled=%s",
			startedID, routedID, handledID)
	}
}

func TestResponseWriterCapture(t *testing.T) {
	collector := newTestEventCollector()

	router := NewV2()
	router.SetInstanceEventDispatcher(collector.dispatch)
	router.Get("/content", func(c *Context) error {
		return c.String(http.StatusCreated, "Hello, World!")
	})

	req := httptest.NewRequest(http.MethodGet, "/content", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	// Verify status code and bytes written were captured
	handled := collector.findEvent(func(e interface{}) bool {
		if rh, ok := e.(*RequestHandled); ok {
			return rh.StatusCode == http.StatusCreated && rh.BytesWritten > 0
		}
		return false
	})
	if handled == nil {
		t.Error("Response metrics not captured correctly")
	}
}

func TestEventNames(t *testing.T) {
	tests := []struct {
		event    interface{ Name() string }
		expected string
	}{
		{&RequestStarted{}, "request.started"},
		{&RequestRouted{}, "request.routed"},
		{&RequestHandled{}, "request.handled"},
		{&RequestFailed{}, "request.failed"},
	}

	for _, tt := range tests {
		if got := tt.event.Name(); got != tt.expected {
			t.Errorf("Event name = %v, want %v", got, tt.expected)
		}
	}
}
