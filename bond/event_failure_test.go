package bond

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
func TestHTTPGateway_FailedEventDispatchLoggedOncePerEvent(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	g := &HTTPGateway{}
	g.SetEventDispatcher(func(context.Context, interface{}) error { return errors.New("listener failed") })
	for i := 0; i < 2; i++ {
		if _, err := g.handleFailure(context.Background(), Page{Component: "Home"}, ssrServerError{}, errors.New("ssr down")); err != nil {
			t.Fatalf("handleFailure: %v", err)
		}
	}
	if n := strings.Count(out.String(), "WARN event dispatch failed"); n != 1 || !strings.Contains(out.String(), "event="+EventSSRRenderFailed) {
		t.Errorf("fallback output = %q, want one warn line naming %s", out.String(), EventSSRRenderFailed)
	}
}
