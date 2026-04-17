package csrf

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestSessionFallback_DispatchedWhenNoSessionCookie(t *testing.T) {
	c := New(DefaultConfig())

	var mu sync.Mutex
	var events []interface{}
	c.SetEventDispatcher(func(event interface{}) error {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	})

	r := httptest.NewRequest("POST", "/submit", nil)
	// No session cookie set — should trigger fallback event.
	if _, err := c.getSessionID(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt, ok := events[0].(*SessionFallback)
	if !ok {
		t.Fatalf("expected *SessionFallback, got %T", events[0])
	}
	if evt.Name() != "csrf.session_fallback" {
		t.Errorf("unexpected event name: %s", evt.Name())
	}
	if evt.Path != "/submit" || evt.Method != "POST" {
		t.Errorf("unexpected path/method: %s %s", evt.Method, evt.Path)
	}
}

func TestSessionFallback_NotDispatchedWithCookie(t *testing.T) {
	c := New(DefaultConfig())
	var count int
	c.SetEventDispatcher(func(event interface{}) error {
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
		t.Errorf("fallback event fired unexpectedly: count=%d", count)
	}
}

// cookie constructs a minimal http.Cookie helper to avoid importing net/http
// repetition at the top of the test file.
func cookie(name, value string) http.Cookie {
	return http.Cookie{Name: name, Value: value}
}
