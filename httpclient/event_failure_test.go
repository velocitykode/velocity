package httpclient

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// A failed event dispatch goes to the one failure policy: counted, and the
// first failure of each event name logged at warn level through the
// component's logger (the standalone fallback here), not ignored.
func TestClient_FailedEventDispatchLoggedOncePerEvent(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	c := New()
	c.SetEventDispatcher(func(context.Context, interface{}) error { return errors.New("listener failed") })
	for i := 0; i < 2; i++ {
		c.dispatchRequestSent(context.Background(), "GET", "https://example.test/", 200, 0, 0, 0)
	}
	if n := strings.Count(out.String(), "WARN event dispatch failed"); n != 1 || !strings.Contains(out.String(), "event=httpclient.request.completed") {
		t.Errorf("fallback output = %q, want one warn line naming httpclient.request.completed", out.String())
	}
}
