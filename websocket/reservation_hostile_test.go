package websocket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// An auth function that panics gives back the connection slot and the
// pump slots it would have held, so Shutdown does not wait out its
// deadline on a connection that never started.
func TestServer_PanickingAuthFuncLeaksNoReservation(t *testing.T) {
	s := New(Config{AuthFunc: func(*http.Request) error { panic("auth broke") }})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	p := hostile.Within(t, hostile.Deadline, func() {
		s.HandleConnection(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ws", nil))
	})
	if p == nil {
		t.Fatal("premise: the auth function's panic did not reach the caller")
	}
	if got := s.activeConns.Load(); got != 0 {
		t.Fatalf("active connections = %d after the panic, want 0", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v, want nil: a pump slot leaked", err)
	}
}
