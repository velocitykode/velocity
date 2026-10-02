package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/cache/drivers"
	cacheredis "github.com/velocitykode/velocity/cache/redis"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/csrf/stores"
)

type csrfBagKey struct{}

// csrfWithSessionBag returns a CSRF instance whose token lives in the
// session the request context carries, as velocity.New wires it.
func csrfWithSessionBag(t *testing.T) *csrf.CSRF {
	t.Helper()
	c, _ := csrfWithSessionBagStore(t, false)
	return c
}

func csrfWithSessionBagStore(t *testing.T, singleUse bool) (*csrf.CSRF, *stores.SessionBagStore) {
	t.Helper()
	cfg := csrf.DefaultConfig()
	store := stores.NewSessionBagStore(func(ctx context.Context) stores.SessionBag {
		s, _ := ctx.Value(csrfBagKey{}).(auth.Session)
		if s == nil {
			return nil
		}
		return s
	}, time.Hour)
	cfg.Store = store
	cfg.SingleUse = singleUse
	cfg.SessionIDResolver = func(*http.Request) (string, error) { return "", csrf.ErrNoSession }
	c, err := csrf.NewE(cfg)
	if err != nil {
		t.Fatalf("csrf.NewE: %v", err)
	}
	t.Cleanup(func() { _ = c.Shutdown(context.Background()) })
	return c, store
}

func servedUnder(s auth.Session) context.Context {
	return context.WithValue(context.Background(), csrfBagKey{}, s)
}

// Concurrent first token reads of one session, in requests that each
// loaded the session before any of them saved (tabs opened at once after
// the token was revoked), hand out one token, and the record keeps it:
// every page's token is accepted afterwards. Minting in each request and
// saving last-write-wins accepted only the last page's token.
func TestServerStore_ConcurrentCSRFMintAfterRevokeHandsOutOneToken(t *testing.T) {
	const n = 32
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, err := NewServerStore(testConfig(), backend.new(t))
			if err != nil {
				t.Fatalf("NewServerStore: %v", err)
			}
			c := csrfWithSessionBag(t)

			first, _ := store.Create("")
			first.Put("cart", "one item")
			old, err := c.GetToken(servedUnder(first), first.ID())
			if err != nil {
				t.Fatalf("GetToken: %v", err)
			}
			if err := first.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save: %v", err)
			}
			id := first.ID()

			revoking := loadSession(t, store, id)
			if err := c.RevokeToken(servedUnder(revoking), id); err != nil {
				t.Fatalf("RevokeToken: %v", err)
			}
			if err := revoking.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save: %v", err)
			}

			sessions := make([]auth.Session, n)
			for i := range sessions {
				sessions[i] = loadSession(t, store, id)
			}
			tokens := make([]string, n)
			errs := make([]error, n)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i, s := range sessions {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					tokens[i], errs[i] = c.GetToken(servedUnder(s), id)
					if errs[i] == nil {
						errs[i] = s.Save(httptest.NewRecorder())
					}
				}()
			}
			close(start)
			wg.Wait()

			for i := range tokens {
				if errs[i] != nil {
					t.Fatalf("request %d: %v", i, errs[i])
				}
				if tokens[i] != tokens[0] {
					t.Fatalf("request %d got token %q, request 0 got %q: one session minted more than one token", i, tokens[i], tokens[0])
				}
			}
			if tokens[0] == old {
				t.Fatal("the revoked token was handed out again")
			}
			after := loadSession(t, store, id)
			if got := after.Get(stores.TokenSessionKey); got != tokens[0] {
				t.Fatalf("record holds %v, every request handed out %q", got, tokens[0])
			}
			if after.Get("cart") != "one item" {
				t.Fatalf("the mint lost another key of the session: cart = %v", after.Get("cart"))
			}
		})
	}
}

var _ stores.SharedBag = (*ServerSession)(nil)

// savedSessionWithToken saves a session holding a token and returns its id
// and the token.
func savedSessionWithToken(t *testing.T, store *ServerStore, c *csrf.CSRF) (string, string) {
	t.Helper()
	s, _ := store.Create("")
	tok, err := c.GetToken(servedUnder(s), s.ID())
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if err := s.Save(httptest.NewRecorder()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return s.ID(), tok
}

// revokedSession returns a saved session whose token was revoked.
func revokedSession(t *testing.T, store *ServerStore, c *csrf.CSRF) string {
	t.Helper()
	id, _ := savedSessionWithToken(t, store, c)
	s := loadSession(t, store, id)
	if err := c.RevokeToken(servedUnder(s), id); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if err := s.Save(httptest.NewRecorder()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return id
}

func tokenInRecord(t *testing.T, store *ServerStore, id string) any {
	t.Helper()
	return loadSession(t, store, id).Get(stores.TokenSessionKey)
}

// Requests that all loaded the session before any of them minted, then
// mint and save one after another (no two mints overlap in time), hand out
// one token: the mint reads the record, not the request's snapshot. Each
// request's own change survives, and the mint does not clean the session.
func TestServerStore_StaggeredCSRFMintsFromPreloadedSessions(t *testing.T) {
	const n = 8
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, err := NewServerStore(testConfig(), backend.new(t))
			if err != nil {
				t.Fatalf("NewServerStore: %v", err)
			}
			c := csrfWithSessionBag(t)
			id := revokedSession(t, store, c)

			sessions := make([]auth.Session, n)
			for i := range sessions {
				sessions[i] = loadSession(t, store, id)
				sessions[i].Put(fmt.Sprintf("tab-%d", i), i)
			}
			var first string
			for i, s := range sessions {
				tok, err := c.GetToken(servedUnder(s), id)
				if err != nil {
					t.Fatalf("request %d: %v", i, err)
				}
				if i == 0 {
					first = tok
				} else if tok != first {
					t.Fatalf("request %d got %q, request 0 got %q", i, tok, first)
				}
				if !s.(*ServerSession).IsModified() {
					t.Fatalf("request %d: the mint marked the session clean, dropping its own change", i)
				}
				if err := s.Save(httptest.NewRecorder()); err != nil {
					t.Fatalf("Save %d: %v", i, err)
				}
			}
			after := loadSession(t, store, id)
			if got := after.Get(stores.TokenSessionKey); got != first {
				t.Fatalf("record holds %v, every request handed out %q", got, first)
			}
			for i := range n {
				if after.Get(fmt.Sprintf("tab-%d", i)) == nil {
					t.Fatalf("request %d's own key was lost", i)
				}
			}
		})
	}
}

// Two instances sharing one record backend (the redis-backed CacheStore,
// and a CacheStore over one shared memory cache): concurrent first reads
// on both, each instance with its own CSRF store, hand out one token. The
// record store's compare-and-set retries the losing writes.
func TestServerStore_CSRFMintAcrossInstances(t *testing.T) {
	const perInstance = 8
	for _, backend := range []struct {
		name string
		new  func(t *testing.T) (auth.ServerSessionStore, auth.ServerSessionStore)
	}{
		{"cache records over one memory cache", func(t *testing.T) (auth.ServerSessionStore, auth.ServerSessionStore) {
			b := drivers.NewMemoryStore("sessions")
			t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
			return newCacheStore(t, b), newCacheStore(t, b)
		}},
		{"cache records over one redis", func(t *testing.T) (auth.ServerSessionStore, auth.ServerSessionStore) {
			mr := miniredis.RunT(t)
			open := func() auth.ServerSessionStore {
				b, err := cacheredis.NewRedisStore(context.Background(), "sessions", mr.Host(), mr.Server().Addr().Port, "", 0, false)
				if err != nil {
					t.Fatalf("NewRedisStore: %v", err)
				}
				t.Cleanup(func() { _ = b.Shutdown(context.Background()) })
				return newCacheStore(t, b)
			}
			return open(), open()
		}},
	} {
		t.Run(backend.name, func(t *testing.T) {
			ra, rb := backend.new(t)
			storeA, err := NewServerStore(testConfig(), ra)
			if err != nil {
				t.Fatal(err)
			}
			storeB, err := NewServerStore(testConfig(), rb)
			if err != nil {
				t.Fatal(err)
			}
			cA, cB := csrfWithSessionBag(t), csrfWithSessionBag(t)
			id := revokedSession(t, storeA, cA)

			type req struct {
				s auth.Session
				c *csrf.CSRF
			}
			var reqs []req
			for range perInstance {
				reqs = append(reqs, req{loadSession(t, storeA, id), cA}, req{loadSession(t, storeB, id), cB})
			}
			tokens := make([]string, len(reqs))
			errs := make([]error, len(reqs))
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i, r := range reqs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					tokens[i], errs[i] = r.c.GetToken(servedUnder(r.s), id)
					if errs[i] == nil {
						errs[i] = r.s.Save(httptest.NewRecorder())
					}
				}()
			}
			close(start)
			wg.Wait()
			for i := range tokens {
				if errs[i] != nil {
					t.Fatalf("request %d: %v", i, errs[i])
				}
				if tokens[i] != tokens[0] {
					t.Fatalf("request %d got %q, request 0 got %q: the instances minted two tokens", i, tokens[i], tokens[0])
				}
			}
			if got := tokenInRecord(t, storeB, id); got != tokens[0] {
				t.Fatalf("record holds %v, want %q", got, tokens[0])
			}
		})
	}
}

// Single use: request A consumes the token while request B, loaded before
// the consume, still holds it in its snapshot. B mints the next token
// instead of handing out the consumed one, A then reads that same next
// token, and A's late Save neither deletes nor replaces it.
func TestServerStore_SingleUseConsumeMintLateSave(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, err := NewServerStore(testConfig(), backend.new(t))
			if err != nil {
				t.Fatalf("NewServerStore: %v", err)
			}
			c, bagStore := csrfWithSessionBagStore(t, true)
			id, used := savedSessionWithToken(t, store, c)

			post := loadSession(t, store, id)
			page := loadSession(t, store, id)
			if ok, err := bagStore.ConsumeIfMatch(servedUnder(post), id, used); !ok || err != nil {
				t.Fatalf("ConsumeIfMatch = %v, %v", ok, err)
			}
			if ok, _ := bagStore.ConsumeIfMatch(servedUnder(page), id, used); ok {
				t.Fatal("a second request consumed the same token")
			}
			next, err := c.GetToken(servedUnder(page), id)
			if err != nil || next == "" || next == used {
				t.Fatalf("page GetToken = %q, %v; want a fresh token", next, err)
			}
			if err := page.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save page: %v", err)
			}
			post.Put("submitted", true)
			if err := post.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save post: %v", err)
			}
			if got := tokenInRecord(t, store, id); got != next {
				t.Fatalf("record holds %v after the consuming request's late save, want %q", got, next)
			}
		})
	}
}

// A rotation (same id) and a refresh write the record at once: a request
// loaded before them, with a pending deletion of the token key and other
// changes, saves late without restoring or removing the newer token, and
// a mint in it adopts the newer token instead of storing its own.
func TestServerStore_RotationThenLateSave(t *testing.T) {
	for _, backend := range recordBackends() {
		t.Run(backend.name, func(t *testing.T) {
			store, err := NewServerStore(testConfig(), backend.new(t))
			if err != nil {
				t.Fatalf("NewServerStore: %v", err)
			}
			c := csrfWithSessionBag(t)
			id, old := savedSessionWithToken(t, store, c)

			late := loadSession(t, store, id)
			late.Remove(stores.TokenSessionKey) // a pending deletion of the key
			late.Put("draft", "hello")

			rotating := loadSession(t, store, id)
			if err := c.RotateToken(servedUnder(rotating), id, id); err != nil {
				t.Fatalf("RotateToken: %v", err)
			}
			rotated, _ := rotating.Get(stores.TokenSessionKey).(string)
			if rotated == "" || rotated == old {
				t.Fatalf("rotation left %q", rotated)
			}
			if got := tokenInRecord(t, store, id); got != rotated {
				t.Fatalf("rotation did not reach the record: %v", got)
			}

			got, err := c.GetToken(servedUnder(late), id)
			if err != nil || got != rotated {
				t.Fatalf("late request's mint = %q, %v; want the rotated token %q", got, err, rotated)
			}
			if err := late.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save late: %v", err)
			}
			after := loadSession(t, store, id)
			if after.Get(stores.TokenSessionKey) != rotated || after.Get("draft") != "hello" {
				t.Fatalf("after the late save: token %v, draft %v; want %q, hello", after.Get(stores.TokenSessionKey), after.Get("draft"), rotated)
			}

			// A request loaded before a revoke saves late: the revoke stays.
			stale := loadSession(t, store, id)
			stale.Put("x", 1)
			revoking := loadSession(t, store, id)
			if err := c.RevokeToken(servedUnder(revoking), id); err != nil {
				t.Fatalf("RevokeToken: %v", err)
			}
			if err := stale.Save(httptest.NewRecorder()); err != nil {
				t.Fatalf("Save stale: %v", err)
			}
			if got := tokenInRecord(t, store, id); got != nil {
				t.Fatalf("a late save restored the revoked token: %v", got)
			}
		})
	}
}

// A session not saved yet takes the local path; a session whose saved
// record is gone fails the mint and is not recreated; a sealed session is
// not written.
func TestServerStore_CSRFMintMissingAndSealedSessions(t *testing.T) {
	records := NewMemoryStore()
	t.Cleanup(func() { _ = records.Close(context.Background()) })
	store, err := NewServerStore(testConfig(), records)
	if err != nil {
		t.Fatal(err)
	}
	c := csrfWithSessionBag(t)

	fresh, _ := store.Create("")
	tok, err := c.GetToken(servedUnder(fresh), fresh.ID())
	if err != nil || tok == "" || fresh.Get(stores.TokenSessionKey) != tok {
		t.Fatalf("unsaved session: %q, %v; session holds %v", tok, err, fresh.Get(stores.TokenSessionKey))
	}
	if _, err := records.Get(context.Background(), fresh.ID()); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("the mint created a record for an unsaved session: %v", err)
	}

	id := revokedSession(t, store, c)
	gone := loadSession(t, store, id)
	if err := records.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetToken(servedUnder(gone), id); !errors.Is(err, contract.ErrSessionRecordGone) {
		t.Fatalf("GetToken on a session whose record is gone = %v, want ErrSessionRecordGone", err)
	}
	if _, err := records.Get(context.Background(), id); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("the mint recreated a revoked record: %v", err)
	}
	if err := c.RevokeToken(servedUnder(gone), id); err != nil {
		t.Fatalf("RevokeToken on a gone record = %v, want nil", err)
	}

	id = revokedSession(t, store, c)
	sealed := loadSession(t, store, id)
	sealed.(*ServerSession).Seal()
	if _, err := c.GetToken(servedUnder(sealed), id); !errors.Is(err, stores.ErrSessionSealed) {
		t.Fatalf("GetToken on a sealed session = %v, want ErrSessionSealed", err)
	}
	if got := tokenInRecord(t, store, id); got != nil {
		t.Fatalf("a sealed session wrote the record: %v", got)
	}
}

// failingRecords is a record store whose UpdateData fails.
type failingRecords struct {
	auth.ServerSessionStore
	err error
}

func (f failingRecords) UpdateData(context.Context, string, func(map[string]any) (map[string]any, error), time.Time, time.Time) error {
	return f.err
}

// A record store failure, and an error from the update itself, reach the
// caller and leave the session as it is.
func TestServerSession_UpdateSharedFailures(t *testing.T) {
	records := NewMemoryStore()
	t.Cleanup(func() { _ = records.Close(context.Background()) })
	store, err := NewServerStore(testConfig(), records)
	if err != nil {
		t.Fatal(err)
	}
	c := csrfWithSessionBag(t)
	id := revokedSession(t, store, c)
	loaded := loadSession(t, store, id).(*ServerSession)

	refused := errors.New("refused")
	_, _, err = loaded.UpdateShared(context.Background(), "k", func(any, bool) (any, bool, error) { return nil, false, refused })
	if !errors.Is(err, refused) || loaded.Get("k") != nil {
		t.Fatalf("update error: %v; session holds %v", err, loaded.Get("k"))
	}

	boom := errors.New("records down")
	store.SetServerSessionStore(failingRecords{ServerSessionStore: records, err: boom})
	if _, err := c.GetToken(servedUnder(loaded), id); !errors.Is(err, boom) {
		t.Fatalf("GetToken over a failing record store = %v, want %v", err, boom)
	}
	if got := loaded.Get(stores.TokenSessionKey); got != nil {
		t.Fatalf("a failed shared write put %v in the session", got)
	}
}

// A shared write on one goroutine of a request and the request's Save on
// another share the session's save base without a data race, and the
// written value ends in the record.
func TestServerSession_UpdateSharedRacingSave(t *testing.T) {
	records := NewMemoryStore()
	t.Cleanup(func() { _ = records.Close(context.Background()) })
	store, err := NewServerStore(testConfig(), records)
	if err != nil {
		t.Fatal(err)
	}
	c := csrfWithSessionBag(t)
	id, _ := savedSessionWithToken(t, store, c)
	set := func(any, bool) (any, bool, error) { return "v", true, nil }
	for i := 0; i < 50; i++ {
		s := loadSession(t, store, id).(*ServerSession)
		s.Put("n", i)
		var wg sync.WaitGroup
		var held any
		var upErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			held, _, upErr = s.UpdateShared(context.Background(), "other", set)
		}()
		go func() {
			defer wg.Done()
			_ = s.Save(httptest.NewRecorder())
		}()
		wg.Wait()
		if upErr != nil || held != "v" {
			t.Fatalf("UpdateShared racing Save = %v, %v", held, upErr)
		}
		_ = s.Save(httptest.NewRecorder())
		if got := loadSession(t, store, id).Get("other"); got != "v" {
			t.Fatalf("iteration %d: record holds %v, want v", i, got)
		}
		_, _, _ = loadSession(t, store, id).(*ServerSession).UpdateShared(context.Background(), "other", func(any, bool) (any, bool, error) { return nil, false, nil })
	}
}
