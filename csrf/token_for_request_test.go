package csrf

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/csrf/stores"
)

// countingStore wraps a real Store and counts Get / Set calls so the
// memoization test can assert that TokenForRequest collapses N reader
// calls onto exactly one underlying Store.Get.
type countingStore struct {
	inner Store

	getCalls atomic.Int64
	setCalls atomic.Int64
}

func newCountingStore() *countingStore {
	return &countingStore{inner: stores.NewSessionStore()}
}

func (s *countingStore) Get(id string) (string, error) {
	s.getCalls.Add(1)
	return s.inner.Get(id)
}

func (s *countingStore) Set(id, token string) error {
	s.setCalls.Add(1)
	return s.inner.Set(id, token)
}

func (s *countingStore) Delete(id string) error { return s.inner.Delete(id) }
func (s *countingStore) Exists(id string) bool  { return s.inner.Exists(id) }

// buildTestCSRF returns a CSRF instance with the given store and a
// SessionIDResolver that reads "session_id" from the request cookie
// jar. Mirrors testConfig but lets the caller swap the store.
func buildTestCSRF(t *testing.T, store Store) *CSRF {
	t.Helper()
	cfg := DefaultConfig()
	cfg.SessionIDResolver = testCookieResolver("session_id")
	cfg.Store = store
	c, err := NewE(cfg)
	if err != nil {
		t.Fatalf("NewE: %v", err)
	}
	return c
}

// requestWithSession builds a request that carries the named session_id
// cookie so testCookieResolver returns sessionID.
func requestWithSession(method, path, sessionID string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})
	return r
}

// TestTokenForRequest_MemoizesAcrossCalls pins the core memoisation
// guarantee: every TokenForRequest call within the same request hits
// the underlying Store at most once and returns byte-identical tokens
// across every reader.
//
// Pre-helper code did `csrf.GetToken(sessionID)` twice per request
// (once in the safe-method bootstrap path, once in the consumer's
// sharePropsFunc), which paid two Store.Get round trips. Under a
// transient store inconsistency, the second call could mint a different
// token and the rendered <meta> tag would disagree with the page-prop,
// surfacing as 419 on the first POST.
//
// Assertions:
//   - Store.Get called exactly ONCE across 10 TokenForRequest calls.
//   - All 10 returns byte-identical.
//   - The returned token is non-empty (a token actually was minted).
func TestTokenForRequest_MemoizesAcrossCalls(t *testing.T) {
	store := newCountingStore()
	c := buildTestCSRF(t, store)

	r := requestWithSession(http.MethodGet, "/", "sess-A")
	r = r.WithContext(WithCSRFTokenState(r.Context(), c))

	const calls = 10
	tokens := make([]string, calls)
	for i := 0; i < calls; i++ {
		tok, err := TokenForRequest(r)
		if err != nil {
			t.Fatalf("TokenForRequest[%d]: %v", i, err)
		}
		if tok == "" {
			t.Fatalf("TokenForRequest[%d]: empty token", i)
		}
		tokens[i] = tok
	}

	if got := store.getCalls.Load(); got != 1 {
		t.Errorf("Store.Get called %d times across %d TokenForRequest calls; want exactly 1 (memoisation should collapse all subsequent reads)", got, calls)
	}
	for i := 1; i < calls; i++ {
		if tokens[i] != tokens[0] {
			t.Errorf("token[%d] = %q != token[0] = %q (memoisation should return byte-identical strings)", i, tokens[i], tokens[0])
		}
	}
}

// TestTokenForRequest_DifferentRequestsIndependent guarantees that the
// memoisation is per-request (not per-CSRF instance). 100 concurrent
// requests, each carrying a distinct session id, each calling
// TokenForRequest five times. Within a request the tokens are
// byte-identical; across requests they differ (one token per session).
func TestTokenForRequest_DifferentRequestsIndependent(t *testing.T) {
	store := newCountingStore()
	c := buildTestCSRF(t, store)

	const (
		requests    = 100
		callsPerReq = 5
	)
	type result struct {
		sessionID string
		tokens    []string
		errs      []error
	}
	results := make([]result, requests)

	var wg sync.WaitGroup
	wg.Add(requests)
	for i := 0; i < requests; i++ {
		i := i
		go func() {
			defer wg.Done()
			sessionID := fmt.Sprintf("sess-%03d", i)
			r := requestWithSession(http.MethodGet, "/", sessionID)
			r = r.WithContext(WithCSRFTokenState(r.Context(), c))
			toks := make([]string, callsPerReq)
			errs := make([]error, callsPerReq)
			for j := 0; j < callsPerReq; j++ {
				toks[j], errs[j] = TokenForRequest(r)
			}
			results[i] = result{sessionID: sessionID, tokens: toks, errs: errs}
		}()
	}
	wg.Wait()

	// Each request: 5 returns must be byte-identical, all non-empty,
	// all err == nil.
	for i, res := range results {
		for j, err := range res.errs {
			if err != nil {
				t.Errorf("request %d call %d: unexpected error: %v", i, j, err)
			}
		}
		if res.tokens[0] == "" {
			t.Errorf("request %d: empty token", i)
			continue
		}
		for j := 1; j < callsPerReq; j++ {
			if res.tokens[j] != res.tokens[0] {
				t.Errorf("request %d: token[%d] = %q != token[0] = %q (memoisation must be stable within a request)",
					i, j, res.tokens[j], res.tokens[0])
			}
		}
	}

	// Across requests: distinct session ids must produce distinct
	// tokens (CSRF tokens are session-scoped). A duplicate would
	// indicate either entropy collapse OR mistaken cross-request
	// state leakage.
	seen := make(map[string]int, requests)
	for i, res := range results {
		if res.tokens[0] == "" {
			continue
		}
		if prev, ok := seen[res.tokens[0]]; ok {
			t.Errorf("token %q minted under request %d AND request %d (cross-request state leak or entropy collapse)",
				res.tokens[0], prev, i)
		}
		seen[res.tokens[0]] = i
	}
}

// TestTokenForRequest_NoStateReturnsErrNoTokenState pins the documented
// "no middleware ran" outcome. Callers that bypass the CSRF middleware
// (gRPC bridge, unit test, custom mux) receive ErrNoTokenState so they
// can choose to render the page anyway (no CSRF token) or surface the
// configuration gap.
func TestTokenForRequest_NoStateReturnsErrNoTokenState(t *testing.T) {
	r := requestWithSession(http.MethodGet, "/", "sess-X")
	tok, err := TokenForRequest(r)
	if !errors.Is(err, ErrNoTokenState) {
		t.Fatalf("err = %v, want ErrNoTokenState", err)
	}
	if tok != "" {
		t.Errorf("token = %q, want empty when no state attached", tok)
	}
}

// TestTokenForRequest_NoSessionReturnsEmptyNilErr pins the documented
// "anonymous request" outcome. When the SessionIDResolver returns
// ErrNoSession (no session cookie), TokenForRequest returns empty +
// nil so a template that conditionally renders <meta csrf-token>
// behaves correctly without raising 5xx.
func TestTokenForRequest_NoSessionReturnsEmptyNilErr(t *testing.T) {
	store := newCountingStore()
	c := buildTestCSRF(t, store)

	// No session cookie attached.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(WithCSRFTokenState(r.Context(), c))

	tok, err := TokenForRequest(r)
	if err != nil {
		t.Fatalf("TokenForRequest: %v", err)
	}
	if tok != "" {
		t.Errorf("token = %q, want empty (anonymous request)", tok)
	}

	// Calling again must still return the same outcome and MUST NOT
	// re-invoke the resolver / store.
	if _, err := TokenForRequest(r); err != nil {
		t.Fatalf("second TokenForRequest: %v", err)
	}
	if got := store.getCalls.Load(); got != 0 {
		t.Errorf("Store.Get called %d times on an anonymous request; want 0 (no session => no token to mint)", got)
	}
}

// TestMiddleware_AttachesTokenStateAutomatically pins that the standard
// CSRF middleware attaches the request-scoped token state, so the
// hot path (handler downstream of csrf.Middleware) does not need to
// know about WithCSRFTokenState.
func TestMiddleware_AttachesTokenStateAutomatically(t *testing.T) {
	store := newCountingStore()
	c := buildTestCSRF(t, store)

	var capturedToken1, capturedToken2 string
	var capturedErr1, capturedErr2 error
	handler := c.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		// Simulate the meta-tag publisher AND the sharePropsFunc both
		// asking for the token within the same request.
		capturedToken1, capturedErr1 = TokenForRequest(req)
		capturedToken2, capturedErr2 = TokenForRequest(req)
	}))

	r := requestWithSession(http.MethodGet, "/", "sess-mw")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if capturedErr1 != nil || capturedErr2 != nil {
		t.Fatalf("TokenForRequest errored after middleware: %v / %v", capturedErr1, capturedErr2)
	}
	if capturedToken1 == "" {
		t.Fatal("TokenForRequest returned empty token after middleware ran")
	}
	if capturedToken1 != capturedToken2 {
		t.Errorf("token diverged between paired reads: %q vs %q", capturedToken1, capturedToken2)
	}
	// Middleware safe-method bootstrap path writes the XSRF cookie,
	// which itself goes through TokenForRequest now, plus the two
	// explicit handler reads = one Store.Get total.
	if got := store.getCalls.Load(); got != 1 {
		t.Errorf("Store.Get called %d times across middleware + 2 handler reads; want 1 (cookie write and both handler reads should share one cached token)", got)
	}
}
