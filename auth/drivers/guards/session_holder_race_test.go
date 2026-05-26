package guards

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// holderRaceUserProvider returns a stable user every time. The test wants
// the cache cells to race, not the user lookup.
type holderRaceUserProvider struct{}

func (p *holderRaceUserProvider) FindByID(id interface{}) (auth.Authenticatable, error) {
	return &auth.AuthUser{ID: id, Name: "Test", Email: "t@example.com"}, nil
}
func (p *holderRaceUserProvider) FindByCredentials(map[string]interface{}) (auth.Authenticatable, error) {
	return &auth.AuthUser{ID: "1", Name: "Test", Email: "t@example.com"}, nil
}
func (p *holderRaceUserProvider) ValidateCredentials(auth.Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *holderRaceUserProvider) UpdateRememberToken(auth.Authenticatable, string) error {
	return nil
}

// holderRaceStore lets a Get block until released and then returns a stable
// StoredSession. The fan-out goroutines below pile into consultServerStore
// concurrently; without mutex protection on the holder cache cells the
// writes to storeOnce/storeRec/storeErr would race the sibling reads.
type holderRaceStore struct {
	user string
}

func (s *holderRaceStore) Get(_ context.Context, id string) (*auth.StoredSession, error) {
	return &auth.StoredSession{ID: id, UserID: s.user}, nil
}
func (s *holderRaceStore) Put(context.Context, *auth.StoredSession) error    { return nil }
func (s *holderRaceStore) Delete(context.Context, string) error              { return nil }
func (s *holderRaceStore) DeleteAllForUser(context.Context, string) error    { return nil }
func (s *holderRaceStore) ListForUser(context.Context, string) ([]*auth.SessionMeta, error) {
	return nil, nil
}

// TestSessionHolder_ConcurrentAccess_NoRace covers the audit M-06 finding:
// the request-scoped sessionHolder's cache cells (session, storeOnce,
// storeRec, storeErr) used to be mutated from fan-out goroutines without
// mutex protection. `go test -race` catches that.
//
// Test method: build a SessionGuard with a server-side store, plant a
// session id, fan out N goroutines that all call User(req) on the SAME
// request (so they all hit the same holder) and let them race.
func TestSessionHolder_ConcurrentAccess_NoRace(t *testing.T) {
	store := &mockSessionStore{}
	guard := &SessionGuard{
		store:  store,
		config: auth.SessionConfig{Name: "test_session"},
		hasher: auth.NewBcryptHasher(10),
	}
	guard.provider.Store(&providerHolder{p: &holderRaceUserProvider{}})
	guard.throttler.Store(&throttlerHolder{t: auth.NoopLoginThrottler{}})
	guard.SetServerSessionStore(&holderRaceStore{user: "1"})

	// Seed the holder via WithSessionContext so every goroutine sees the
	// same *sessionHolder pointer.
	req := WithSessionContext(httptest.NewRequest(http.MethodGet, "/", nil))

	// Plant a session and user_id so User() reaches consultServerStore.
	sess := newMockSession()
	sess.data["user_id"] = "1"
	holder, ok := req.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if !ok || holder == nil {
		t.Fatal("WithSessionContext did not install a holder")
	}
	holder.setSession(sess)

	const goroutines = 64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			// User() reads session, calls consultServerStore which
			// writes storeOnce/storeRec on first call and reads
			// them on every subsequent call. With M-06 unfixed,
			// -race would flag a data race here.
			_ = guard.User(req)
		}()
	}
	wg.Wait()

	// Sanity: the cache was populated by at least one of the goroutines.
	once, rec, err := holder.getStoreCache()
	if !once {
		t.Error("expected storeOnce=true after concurrent User() calls")
	}
	if err != nil {
		t.Errorf("unexpected cached store error: %v", err)
	}
	if rec == nil {
		t.Error("expected cached StoredSession after concurrent User() calls")
	}
}

// TestSessionHolder_ResetStoreCache_NoRace covers the anchorRecalledUser
// path that resets the cache concurrently with consultServerStore readers.
func TestSessionHolder_ResetStoreCache_NoRace(t *testing.T) {
	holder := &sessionHolder{}

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			holder.setStoreCache(&auth.StoredSession{ID: "x"}, nil)
		}()
		go func() {
			defer wg.Done()
			holder.resetStoreCache()
		}()
	}
	wg.Wait()
}
