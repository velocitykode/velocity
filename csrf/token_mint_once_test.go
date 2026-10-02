package csrf

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/csrf/stores"
)

// barrierStore is a shared token store whose Get holds every caller until
// n of them are inside it, so all n read "no token" before any of them
// writes: the widest window a read-then-write mint can be raced in.
type barrierStore struct {
	inner   *stores.MemoryStore
	n       int
	arrived atomic.Int64
	open    chan struct{}
	once    sync.Once
	inserts atomic.Int64 // LoadOrStore calls that stored their candidate
	sets    atomic.Int64
}

func newBarrierStore(n int) *barrierStore {
	return &barrierStore{inner: stores.NewMemoryStore(), n: n, open: make(chan struct{})}
}

func (s *barrierStore) Get(ctx context.Context, id string) (string, error) {
	tok, err := s.inner.Get(ctx, id)
	if s.arrived.Add(1) == int64(s.n) {
		s.once.Do(func() { close(s.open) })
	}
	select {
	case <-s.open:
	case <-time.After(10 * time.Second):
	}
	return tok, err
}

func (s *barrierStore) Set(ctx context.Context, id, token string) error {
	s.sets.Add(1)
	return s.inner.Set(ctx, id, token)
}

func (s *barrierStore) LoadOrStore(ctx context.Context, id, candidate string) (string, bool, error) {
	held, loaded, err := s.inner.LoadOrStore(ctx, id, candidate)
	if err == nil && !loaded {
		s.inserts.Add(1)
	}
	return held, loaded, err
}

func (s *barrierStore) Delete(ctx context.Context, id string) error { return s.inner.Delete(ctx, id) }
func (s *barrierStore) Exists(ctx context.Context, id string) bool  { return s.inner.Exists(ctx, id) }

// N concurrent first reads of one session, every one of which reads "no
// token" before any writes (after a revoke), all get one token and the
// store holds exactly that one: the mint is an insert-if-absent, not a
// read followed by a blind write. A store Get that blocks until every
// reader is inside it does not deadlock the mint.
func TestGetToken_ConcurrentMissesAfterRevokeStoreOneToken(t *testing.T) {
	const n = 32
	const id = "shared-session"
	store := newBarrierStore(n)
	cfg := testConfig()
	cfg.Store = store
	c, err := NewE(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := store.inner.Set(ctx, id, "previous-token"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := c.RevokeToken(ctx, id); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	tokens := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], errs[i] = c.GetToken(ctx, id)
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("concurrent GetToken calls did not return: the mint deadlocked")
	}

	for i := range tokens {
		if errs[i] != nil {
			t.Fatalf("reader %d: %v", i, errs[i])
		}
		if tokens[i] != tokens[0] {
			t.Fatalf("reader %d got %q, reader 0 got %q: one session minted more than one token", i, tokens[i], tokens[0])
		}
	}
	if tokens[0] == "" || tokens[0] == "previous-token" {
		t.Fatalf("token after revoke = %q", tokens[0])
	}
	if got := store.inserts.Load(); got != 1 {
		t.Fatalf("%d tokens stored, want exactly 1", got)
	}
	if got := store.sets.Load(); got != 0 {
		t.Fatalf("Set called %d times: the mint must go through LoadOrStore", got)
	}
	held, err := store.inner.Get(ctx, id)
	if err != nil || held != tokens[0] {
		t.Fatalf("store holds %q (%v), every reader got %q", held, err, tokens[0])
	}
}

// A refresh replaces the token the request's cache already holds (the
// safe-method bootstrap loaded it): a later reader of the same request
// carries the refreshed token, as after RotateToken.
func TestRefreshHandler_SyncsTheRequestTokenCache(t *testing.T) {
	store := stores.NewMemoryStore()
	cfg := DefaultConfig()
	cfg.Store = store
	cfg.SessionIDResolver = func(*http.Request) (string, error) { return "sess", nil }
	c, err := NewE(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var after string
	h := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := TokenForRequest(r); err != nil {
			t.Errorf("TokenForRequest before refresh: %v", err)
		}
		c.RefreshHandler()(w, r)
		after, err = TokenForRequest(r)
		if err != nil {
			t.Errorf("TokenForRequest after refresh: %v", err)
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/csrf/refresh", nil))
	stored, err := store.Get(context.Background(), "sess")
	if err != nil {
		t.Fatal(err)
	}
	if UnmaskToken(after) != stored {
		t.Fatalf("the request's cache carries %q after the refresh, the store holds %q", UnmaskToken(after), stored)
	}
}
