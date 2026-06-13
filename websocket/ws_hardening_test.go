package websocket

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestSentinelErrors_IsMatchable asserts every server/group method that used to
// return a bare fmt.Errorf string now wraps a package sentinel so callers can
// match with errors.Is while the human-readable context is preserved.
func TestSentinelErrors_IsMatchable(t *testing.T) {
	tests := []struct {
		name    string
		produce func(t *testing.T) error
		want    error
	}{
		{
			name: "SendToClient unknown client",
			produce: func(t *testing.T) error {
				s := New(DefaultConfig())
				return s.SendToClient("missing", Message{Type: "x"})
			},
			want: ErrClientNotFound,
		},
		{
			name: "SendToClient send channel full",
			produce: func(t *testing.T) error {
				s := New(DefaultConfig())
				full := &Client{ID: "c1", Send: make(chan Message, 1), Server: s}
				full.Send <- Message{} // saturate the buffer
				s.clients["c1"] = full
				return s.SendToClient("c1", Message{Type: "x"})
			},
			want: ErrSendChannelFull,
		},
		{
			name: "JoinGroup unknown client",
			produce: func(t *testing.T) error {
				s := New(DefaultConfig())
				return s.JoinGroup("missing", "room")
			},
			want: ErrClientNotFound,
		},
		{
			name: "LeaveGroup unknown client",
			produce: func(t *testing.T) error {
				s := New(DefaultConfig())
				return s.LeaveGroup("missing", "room")
			},
			want: ErrClientNotFound,
		},
		{
			name: "BroadcastToGroup unknown group",
			produce: func(t *testing.T) error {
				s := New(DefaultConfig())
				return s.BroadcastToGroup("ghost", Message{Type: "x"})
			},
			want: ErrGroupNotFound,
		},
		{
			name: "SendToOthersInGroup unknown group",
			produce: func(t *testing.T) error {
				s := New(DefaultConfig())
				return s.SendToOthersInGroup("ghost", "sender", Message{Type: "x"})
			},
			want: ErrGroupNotFound,
		},
		{
			name: "Start already running",
			produce: func(t *testing.T) error {
				s := New(DefaultConfig())
				if err := s.Start(); err != nil {
					t.Fatalf("first Start failed: %v", err)
				}
				defer s.Shutdown(context.Background())
				return s.Start()
			},
			want: ErrServerAlreadyRunning,
		},
		{
			name: "Start after shutdown",
			produce: func(t *testing.T) error {
				s := New(DefaultConfig())
				if err := s.Start(); err != nil {
					t.Fatalf("Start failed: %v", err)
				}
				if err := s.Shutdown(context.Background()); err != nil {
					t.Fatalf("Shutdown failed: %v", err)
				}
				return s.Start()
			},
			want: ErrServerClosed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.produce(t)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want errors.Is(_, %v)", err, tt.want)
			}
		})
	}
}

// TestCheckOrigin_EmptyOriginMatrix locks in the BREAKING change: a missing
// Origin header is governed solely by AllowEmptyOrigin across every
// AllowedOrigins configuration, including the "*" wildcard.
func TestCheckOrigin_EmptyOriginMatrix(t *testing.T) {
	tests := []struct {
		name           string
		allowedOrigins []string
		allowEmpty     bool
		origin         string
		want           bool
	}{
		{name: "no list, empty origin, opt-out", allowedOrigins: nil, allowEmpty: false, origin: "", want: false},
		{name: "no list, empty origin, opt-in", allowedOrigins: nil, allowEmpty: true, origin: "", want: true},
		{name: "list, empty origin, opt-out", allowedOrigins: []string{"https://a.example.com"}, allowEmpty: false, origin: "", want: false},
		{name: "list, empty origin, opt-in", allowedOrigins: []string{"https://a.example.com"}, allowEmpty: true, origin: "", want: true},
		{name: "star list, empty origin, opt-out rejects", allowedOrigins: []string{"*"}, allowEmpty: false, origin: "", want: false},
		{name: "star list, empty origin, opt-in accepts", allowedOrigins: []string{"*"}, allowEmpty: true, origin: "", want: true},
		// Non-empty origins are unaffected by the hoist.
		{name: "star list still accepts any non-empty origin", allowedOrigins: []string{"*"}, allowEmpty: false, origin: "https://anything.example.net", want: true},
		{name: "list accepts a listed origin", allowedOrigins: []string{"https://a.example.com"}, allowEmpty: false, origin: "https://a.example.com", want: true},
		{name: "list rejects an unlisted origin", allowedOrigins: []string{"https://a.example.com"}, allowEmpty: false, origin: "https://evil.example.com", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultConfig()
			config.AllowedOrigins = tt.allowedOrigins
			config.AllowEmptyOrigin = tt.allowEmpty
			s := New(config)

			req := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
			req.Host = "example.com"
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}

			if got := s.checkOrigin(req); got != tt.want {
				t.Errorf("checkOrigin(list=%v empty=%v origin=%q) = %v, want %v",
					tt.allowedOrigins, tt.allowEmpty, tt.origin, got, tt.want)
			}
		})
	}
}

// TestHandleRaw_GatedBeforeRunning verifies the running gate fires before any
// upgrade, returning ErrServerNotRunning (never started) or ErrServerClosed
// (after Shutdown) with an HTTP 503 and a no-op release.
func TestHandleRaw_GatedBeforeRunning(t *testing.T) {
	t.Run("not started", func(t *testing.T) {
		s := New(DefaultConfig())
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)

		conn, release, err := s.HandleRaw(rec, req)
		if !errors.Is(err, ErrServerNotRunning) {
			t.Fatalf("got %v, want ErrServerNotRunning", err)
		}
		if conn != nil {
			t.Error("expected nil conn on rejection")
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
		release() // must be a safe no-op
	})

	t.Run("after shutdown", func(t *testing.T) {
		s := New(DefaultConfig())
		if err := s.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		if err := s.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown failed: %v", err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)

		conn, release, err := s.HandleRaw(rec, req)
		if !errors.Is(err, ErrServerClosed) {
			t.Fatalf("got %v, want ErrServerClosed", err)
		}
		if conn != nil {
			t.Error("expected nil conn on rejection")
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
		release()
	})
}

// TestHandleRaw_RejectsUnauthorized verifies the AuthFunc gate runs before the
// upgrade, returning HTTP 401 and surfacing the underlying auth error.
func TestHandleRaw_RejectsUnauthorized(t *testing.T) {
	authErr := errors.New("nope")
	config := DefaultConfig()
	config.AuthFunc = func(*http.Request) error { return authErr }
	s := New(config)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Shutdown(context.Background())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)

	conn, release, err := s.HandleRaw(rec, req)
	if !errors.Is(err, authErr) {
		t.Fatalf("got %v, want wrapped authErr", err)
	}
	if conn != nil {
		t.Error("expected nil conn on rejection")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	release()
	if got := s.activeConns.Load(); got != 0 {
		t.Errorf("activeConns = %d, want 0 (auth runs before the reservation)", got)
	}
}

// TestHandleRaw_RejectsConnectionLimit verifies MaxConnections is enforced with
// rollback, returning ErrConnectionLimit and not leaking the reservation.
func TestHandleRaw_RejectsConnectionLimit(t *testing.T) {
	config := DefaultConfig()
	config.MaxConnections = 1
	s := New(config)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Shutdown(context.Background())

	// Pre-occupy the single slot so the next admission overflows.
	s.activeConns.Store(1)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)

	conn, release, err := s.HandleRaw(rec, req)
	if !errors.Is(err, ErrConnectionLimit) {
		t.Fatalf("got %v, want ErrConnectionLimit", err)
	}
	if conn != nil {
		t.Error("expected nil conn on rejection")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	release()
	if got := s.activeConns.Load(); got != 1 {
		t.Errorf("activeConns = %d, want 1 (overflow rolled back, pre-occupied slot intact)", got)
	}
}

// TestHandleRaw_SuccessReleaseDecrements verifies a successful raw upgrade
// reserves one slot and the returned release func frees it exactly once.
func TestHandleRaw_SuccessReleaseDecrements(t *testing.T) {
	s := New(DefaultConfig())
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Shutdown(context.Background())

	released := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, release, err := s.HandleRaw(w, r)
		if err != nil {
			t.Errorf("HandleRaw failed: %v", err)
			return
		}
		if got := s.activeConns.Load(); got != 1 {
			t.Errorf("activeConns during raw conn = %d, want 1", got)
		}
		// Idempotent: a double release must not drive the count negative.
		release()
		release()
		conn.Close()
		close(released)
	}))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, originHeader(ts.URL))
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer ws.Close()

	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not complete")
	}

	if got := s.activeConns.Load(); got != 0 {
		t.Errorf("activeConns after release = %d, want 0", got)
	}
}
