package csrf

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/csrf/stores"
	"github.com/velocitykode/velocity/internal/hostile"
)

// codeStore is a token store whose Get runs a test's hostile code first.
type codeStore struct {
	inner Store
	code  atomic.Pointer[hostile.Code]
	gets  atomic.Int64
	sets  atomic.Int64
}

func newCodeStore() *codeStore { return &codeStore{inner: stores.NewMemoryStore()} }

func (s *codeStore) Get(ctx context.Context, id string) (string, error) {
	s.gets.Add(1)
	s.code.Load().Run()
	return s.inner.Get(ctx, id)
}

func (s *codeStore) Set(ctx context.Context, id, token string) error {
	s.sets.Add(1)
	return s.inner.Set(ctx, id, token)
}

func (s *codeStore) Delete(ctx context.Context, id string) error { return s.inner.Delete(ctx, id) }
func (s *codeStore) Exists(ctx context.Context, id string) bool  { return s.inner.Exists(ctx, id) }

// TestTokenForRequest_StoreIsUserCode loads a request's token through a
// store whose Get panics, blocks, or reads the same request's token. The
// store runs with no lock of the request's token cache held: a Get reading
// the token again is refused with an error instead of waiting on itself, a
// blocked Get holds up no other use of the cache, and a panicking one
// reaches the caller and leaves nothing cached, so the next read loads.
func TestTokenForRequest_StoreIsUserCode(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			store := newCodeStore()
			c := buildTestCSRF(t, store)
			if err := store.inner.Set(context.Background(), "sid", "stored-token"); err != nil {
				t.Fatal(err)
			}
			base := requestWithSession("GET", "/", "sid")
			r := base.WithContext(withTokenState(base.Context(), c))
			var innerErr error
			code := hostile.New(t, mode, func() { _, innerErr = TokenForRequest(r) })
			store.code.Store(code)

			type result struct {
				token    string
				err      error
				panicked any
			}
			done := make(chan result, 1)
			go func() { //safe-goroutine: the read under test; its result and panic are read below
				var res result
				defer func() {
					res.panicked = recover()
					done <- res
				}()
				res.token, res.err = TokenForRequest(r)
			}()
			if mode == hostile.Block {
				if !code.AwaitEntered(t) {
					return
				}
				state := tokenStateFromContext(r.Context())
				hostile.Within(t, hostile.Deadline, func() {
					if tok, ok := state.cachedFor("sid"); ok {
						t.Errorf("cachedFor while the load blocks = %q, want nothing cached", tok)
					}
				})
				code.Release()
			}
			var res result
			hostile.Within(t, hostile.Deadline, func() { res = <-done })
			if code.Calls() == 0 {
				t.Fatal("the store's Get never ran")
			}
			switch mode {
			case hostile.Panic:
				if res.panicked != hostile.PanicValue {
					t.Fatalf("panic = %v, want the store's", res.panicked)
				}
			case hostile.Reenter:
				if innerErr == nil {
					t.Error("a read from inside the store's load was not refused")
				}
				fallthrough
			default:
				if res.err != nil || res.panicked != nil || res.token == "" {
					t.Fatalf("TokenForRequest = %q, %v, panic %v", res.token, res.err, res.panicked)
				}
			}
			code.Disarm()
			hostile.Within(t, hostile.Deadline, func() {
				tok, err := TokenForRequest(r)
				if err != nil || tok == "" {
					t.Fatalf("a later read = %q, %v", tok, err)
				}
				if raw := UnmaskToken(tok); raw != "stored-token" {
					t.Fatalf("a later read unmasks to %q; want the stored token", raw)
				}
			})
		})
	}
}

// TestTokenForRequest_ConcurrentFirstReadsLoadOnce races many first reads
// of one request: one store load, one mint, the same bytes for every
// reader.
func TestTokenForRequest_ConcurrentFirstReadsLoadOnce(t *testing.T) {
	for range 20 {
		store := newCodeStore()
		c := buildTestCSRF(t, store)
		base := requestWithSession("GET", "/", "sid")
		r := base.WithContext(withTokenState(base.Context(), c))
		var wg sync.WaitGroup
		tokens := make([]string, 16)
		for i := range tokens {
			wg.Go(func() {
				tok, err := TokenForRequest(r)
				if err != nil {
					t.Error(err)
				}
				tokens[i] = tok
			})
		}
		wg.Wait()
		for _, tok := range tokens[1:] {
			if tok != tokens[0] {
				t.Fatalf("readers got different tokens: %q and %q", tokens[0], tok)
			}
		}
		if n := store.sets.Load(); n != 1 {
			t.Fatalf("store Sets = %d, want one mint", n)
		}
	}
}

// TestTokenForRequest_RotationOvertakesALoad rotates the token while a load
// of the same session is in flight: the load publishes nothing over the
// rotation, and every read returns the rotated token.
func TestTokenForRequest_RotationOvertakesALoad(t *testing.T) {
	store := newCodeStore()
	c := buildTestCSRF(t, store)
	if err := store.inner.Set(context.Background(), "sid", "old-token"); err != nil {
		t.Fatal(err)
	}
	base := requestWithSession("GET", "/", "sid")
	r := base.WithContext(withTokenState(base.Context(), c))
	code := hostile.New(t, hostile.Block, nil)
	store.code.Store(code)
	done := make(chan string, 1)
	go func() {
		tok, _ := TokenForRequest(r)
		done <- tok
	}()
	if !code.AwaitEntered(t) {
		return
	}
	tokenStateFromContext(r.Context()).replaceAfterRotation("sid", "sid", "new-token")
	code.Release()
	var tok string
	hostile.Within(t, hostile.Deadline, func() { tok = <-done })
	later, err := TokenForRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range []string{tok, later} {
		if raw := UnmaskToken(got); raw != "new-token" {
			t.Fatalf("read = %q (unmasked %q), want the rotated token", got, raw)
		}
	}
}
