package schemes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// A failed event dispatch goes to the one failure policy: counted, and the
// first failure of each event name logged at warn level through the
// component's logger (the standalone fallback here), not ignored.
// The login still succeeds.
func TestSessionScheme_FailedRehashEventLoggedOncePerEvent(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	scheme, _ := newRehashScheme(t, true)
	scheme.SetEventDispatcher(func(context.Context, any) error { return errors.New("listener failed") })
	for i := 0; i < 2; i++ {
		ok, err := scheme.Attempt(httptest.NewRecorder(), WithSessionContext(httptest.NewRequest(http.MethodPost, "/login", nil)), map[string]interface{}{
			"email":    "alice@example.com",
			"password": "correct",
		})
		if err != nil || !ok {
			t.Fatalf("Attempt = %v, %v; want a successful login", ok, err)
		}
	}
	got := out.String()
	if n := strings.Count(got, "WARN event dispatch failed"); n != 1 || !strings.Contains(got, "event=auth.password.rehash.needed") {
		t.Errorf("fallback output = %q, want one warn line naming auth.password.rehash.needed", got)
	}
}
