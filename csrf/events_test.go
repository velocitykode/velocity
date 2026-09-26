package csrf

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestSessionMissing_DispatchedWhenNoSession(t *testing.T) {
	c := New(testConfig())

	var mu sync.Mutex
	var events []interface{}
	c.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	})

	r := httptest.NewRequest("POST", "/submit", nil)
	if _, err := c.getSessionID(r); err != ErrNoSession {
		t.Fatalf("expected ErrNoSession, got %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt, ok := events[0].(*SessionMissing)
	if !ok {
		t.Fatalf("expected *SessionMissing, got %T", events[0])
	}
	if evt.Name() != "csrf.session_missing" {
		t.Errorf("unexpected event name: %s", evt.Name())
	}
	if evt.Path != "/submit" || evt.Method != "POST" {
		t.Errorf("unexpected path/method: %s %s", evt.Method, evt.Path)
	}
}

func TestSessionMissing_NotDispatchedWithSession(t *testing.T) {
	c := New(testConfig())
	var count int
	c.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		count++
		return nil
	})

	r := httptest.NewRequest("POST", "/submit", nil)
	sc := cookie("session_id", "xyz")
	r.AddCookie(&sc)
	if _, err := c.getSessionID(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("session_missing event fired for a request with a session: count=%d", count)
	}
}

// Every unsafe request without a session is rejected with 419 and reported
// once as csrf.session_missing, whether it carried a token, a garbled one or
// none. A request with a session but no token is rejected without the event.
func TestSessionMissing_ReportsEveryRejectedSessionlessRequest(t *testing.T) {
	c, err := NewE(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	validToken, err := c.GetToken(context.Background(), "someone-else")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		token     string
		session   bool
		wantEvent int
	}{
		{name: "token, no session", token: validToken, wantEvent: 1},
		{name: "no token, no session", wantEvent: 1},
		{name: "malformed token, no session", token: "!!not-a-token!!", wantEvent: 1},
		{name: "no token, session", session: true, wantEvent: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var names []string
			c.SetEventDispatcher(func(_ context.Context, e interface{}) error {
				if n, ok := e.(interface{ Name() string }); ok {
					mu.Lock()
					names = append(names, n.Name())
					mu.Unlock()
				}
				return nil
			})
			req := httptest.NewRequest(http.MethodPost, "/form", nil)
			if tc.token != "" {
				req.Header.Set(c.config.HeaderName, tc.token)
			}
			if tc.session {
				sc := cookie("session_id", "xyz")
				req.AddCookie(&sc)
			}
			ran := false
			rec := httptest.NewRecorder()
			c.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran = true })).ServeHTTP(rec, req)

			if rec.Code != 419 || ran {
				t.Fatalf("status=%d handlerRan=%v, want 419 and handler not run", rec.Code, ran)
			}
			mu.Lock()
			defer mu.Unlock()
			got := 0
			for _, n := range names {
				if n == "csrf.session_missing" {
					got++
				}
			}
			if got != tc.wantEvent || len(names) != tc.wantEvent {
				t.Errorf("events = %v, want %d csrf.session_missing", names, tc.wantEvent)
			}
		})
	}
}

// cookie constructs a minimal http.Cookie helper to avoid importing net/http
// repetition at the top of the test file.
func cookie(name, value string) http.Cookie {
	return http.Cookie{Name: name, Value: value}
}
