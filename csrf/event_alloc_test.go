package csrf

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCSRF_NoDispatcherBuildsNoEvent requires a session-less request to
// build no SessionMissing event when no event dispatcher is installed.
func TestCSRF_NoDispatcherBuildsNoEvent(t *testing.T) {
	c := New(testConfig())
	req := httptest.NewRequest(http.MethodPost, "/form", nil)
	allocs := testing.AllocsPerRun(100, func() {
		_, _ = c.getSessionID(req)
	})
	if allocs != 0 {
		t.Errorf("getSessionID allocated %.0f times with no dispatcher, want 0", allocs)
	}
}
