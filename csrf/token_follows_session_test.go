package csrf

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/csrf/stores"
)

// TestTokenForRequest_FollowsTheResolvedSession pins that the request
// cache is keyed on the session the request is served under: once the
// resolver answers a new id (a sign-in or recall regenerated the session
// after an earlier read), the next read returns the token of the new
// session, and repeated reads of one session stay byte-identical. The
// post-rotation cookie write for the new id, through a context carrying
// the cache, emits the same bytes.
func TestTokenForRequest_FollowsTheResolvedSession(t *testing.T) {
	var mu sync.Mutex
	current := "old-id"
	cfg := DefaultConfig()
	cfg.Store = stores.NewMemoryStore()
	cfg.SessionIDResolver = func(*http.Request) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		return current, nil
	}
	c, err := NewE(cfg)
	if err != nil {
		t.Fatalf("NewE: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(WithCSRFTokenState(r.Context(), c))

	first, err := TokenForRequest(r)
	if err != nil || first == "" {
		t.Fatalf("first read: %q, %v", first, err)
	}
	if again, _ := TokenForRequest(r); again != first {
		t.Fatalf("second read of the same session drifted: %q vs %q", again, first)
	}

	if err := c.RotateToken(context.Background(), "old-id", "new-id"); err != nil {
		t.Fatalf("RotateToken: %v", err)
	}
	mu.Lock()
	current = "new-id"
	mu.Unlock()

	after, err := TokenForRequest(r)
	if err != nil || after == "" {
		t.Fatalf("read after the session changed: %q, %v", after, err)
	}
	stored, err := cfg.Store.Get(context.Background(), "new-id")
	if err != nil {
		t.Fatalf("store get new-id: %v", err)
	}
	if UnmaskToken(after) != stored {
		t.Fatal("the read after the session changed is not the new session's token")
	}
	if again, _ := TokenForRequest(r); again != after {
		t.Fatalf("repeated read of the new session drifted: %q vs %q", again, after)
	}

	w := httptest.NewRecorder()
	c.WriteXSRFCookie(r.Context(), w, "new-id")
	var cookie *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == "XSRF-TOKEN" {
			cookie = ck
		}
	}
	if cookie == nil {
		t.Fatal("WriteXSRFCookie wrote no XSRF-TOKEN")
	}
	got, err := url.QueryUnescape(cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if got != after {
		t.Fatalf("post-rotation cookie %q differs from the rendered token %q", got, after)
	}
}
