package schemes

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
)

var errOutageStore = errors.New("blacklist backend down")

// outageBlacklist answers from an in-memory store until told to fail its
// reads or its writes, by returning an error or by panicking.
type outageBlacklist struct {
	*auth.InMemoryBlacklistStore
	failRead  atomic.Bool
	failWrite atomic.Bool
	panics    atomic.Bool
}

func (s *outageBlacklist) fail() error {
	if s.panics.Load() {
		panic(errOutageStore)
	}
	return errOutageStore
}

func (s *outageBlacklist) IsBlacklisted(jti string) (bool, error) {
	if s.failRead.Load() {
		return false, s.fail()
	}
	return s.InMemoryBlacklistStore.IsBlacklisted(jti)
}

func (s *outageBlacklist) Add(jti string, expiresAt time.Time) (bool, error) {
	if s.failWrite.Load() {
		return false, s.fail()
	}
	return s.InMemoryBlacklistStore.Add(jti, expiresAt)
}

// outageScheme builds a JWT scheme on store with a user store that counts
// its lookups, and a request carrying a valid access token.
func outageScheme(t testing.TB, store auth.BlacklistStore) (*JWTScheme, *http.Request, *atomic.Int32) {
	t.Helper()
	var lookups atomic.Int32
	users := &mockJWTUserStore{findByIDFunc: func(id interface{}) (contract.Authenticatable, error) {
		lookups.Add(1)
		return &mockJWTUser{id: id}, nil
	}}
	cfg := newTestJWTConfig()
	cfg.BlacklistStore = store
	g := mustNewJWTScheme(users, cfg)
	token, err := g.GenerateToken(&mockJWTUser{id: "user123"})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return g, r, &lookups
}

// A blacklist store that cannot answer refuses the request on every read
// path, without asking the user store, and CheckWithError names the
// outage so it is not mistaken for a bad credential.
func TestJWTScheme_BlacklistOutage_RefusesAndReports(t *testing.T) {
	for _, panics := range []bool{false, true} {
		name := "error"
		if panics {
			name = "panic"
		}
		t.Run(name, func(t *testing.T) {
			store := &outageBlacklist{InMemoryBlacklistStore: auth.NewInMemoryBlacklistStore()}
			g, r, lookups := outageScheme(t, store)

			ok, err := g.CheckWithError(r)
			if !ok || err != nil {
				t.Fatalf("CheckWithError with a working store = (%v, %v), want (true, nil)", ok, err)
			}
			before := lookups.Load()

			store.panics.Store(panics)
			store.failRead.Store(true)

			ok, err = g.CheckWithError(r)
			if ok {
				t.Fatal("CheckWithError authenticated a request while the blacklist could not answer")
			}
			if !errors.Is(err, auth.ErrBlacklistUnavailable) || !errors.Is(err, errOutageStore) {
				t.Fatalf("CheckWithError error = %v, want ErrBlacklistUnavailable wrapping the store's cause", err)
			}
			if g.Check(r) {
				t.Error("Check authenticated a request while the blacklist could not answer")
			}
			// User is asked with the user already cached by the first
			// check: the cache must not stand in for the validity decision.
			if u := g.User(r); u != nil {
				t.Errorf("User = %v while the blacklist could not answer, want nil", u)
			}
			if id := g.ID(r); id != nil {
				t.Errorf("ID = %v while the blacklist could not answer, want nil", id)
			}
			if n := lookups.Load(); n != before {
				t.Errorf("user store asked %d times for a token refused at validation", n-before)
			}

			store.failRead.Store(false)
			if ok, err := g.CheckWithError(r); !ok || err != nil {
				t.Fatalf("CheckWithError after the store recovered = (%v, %v), want (true, nil)", ok, err)
			}
		})
	}
}

// Ordinary refusals carry no error: no token, a malformed token, a revoked
// token, a user that is gone.
func TestJWTScheme_CheckWithError_OrdinaryRefusals(t *testing.T) {
	store := &outageBlacklist{InMemoryBlacklistStore: auth.NewInMemoryBlacklistStore()}
	g, r, _ := outageScheme(t, store)

	bare := httptest.NewRequest(http.MethodGet, "/", nil)
	if ok, err := g.CheckWithError(bare); ok || err != nil {
		t.Errorf("no token: CheckWithError = (%v, %v), want (false, nil)", ok, err)
	}

	garbage := httptest.NewRequest(http.MethodGet, "/", nil)
	garbage.Header.Set("Authorization", "Bearer not.a.token")
	if ok, err := g.CheckWithError(garbage); ok || err != nil {
		t.Errorf("malformed token: CheckWithError = (%v, %v), want (false, nil)", ok, err)
	}

	gone := mustNewJWTScheme(&mockJWTUserStore{findByIDFunc: func(interface{}) (contract.Authenticatable, error) {
		return nil, nil
	}}, func() auth.JWTConfig {
		cfg := newTestJWTConfig()
		cfg.BlacklistStore = store
		return cfg
	}())
	if ok, err := gone.CheckWithError(r); ok || err != nil {
		t.Errorf("user gone: CheckWithError = (%v, %v), want (false, nil)", ok, err)
	}

	if err := g.Logout(httptest.NewRecorder(), r); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if ok, err := g.CheckWithError(r); ok || err != nil {
		t.Errorf("revoked token: CheckWithError = (%v, %v), want (false, nil)", ok, err)
	}
}

// A store that cannot take the revocation: Logout still ends the user's
// refresh tokens and drops the cached user, and returns the outage.
func TestJWTScheme_Logout_RevokeFails_StillBumpsGenerationAndReports(t *testing.T) {
	for _, panics := range []bool{false, true} {
		name := "error"
		if panics {
			name = "panic"
		}
		t.Run(name, func(t *testing.T) {
			store := &outageBlacklist{InMemoryBlacklistStore: auth.NewInMemoryBlacklistStore()}
			g, r, _ := outageScheme(t, store)
			user := &mockJWTUser{id: "user123"}
			refresh, err := g.GenerateRefreshToken(user)
			if err != nil {
				t.Fatalf("GenerateRefreshToken: %v", err)
			}
			if g.User(r) == nil {
				t.Fatal("test setup error: the token does not authenticate")
			}
			token := g.getTokenFromRequest(r)
			if _, ok := g.getCachedUser(token); !ok {
				t.Fatal("test setup error: the user is not cached")
			}

			store.panics.Store(panics)
			store.failWrite.Store(true)
			err = g.Logout(httptest.NewRecorder(), r)
			if !errors.Is(err, auth.ErrBlacklistUnavailable) || !errors.Is(err, errOutageStore) {
				t.Fatalf("Logout error = %v, want ErrBlacklistUnavailable wrapping the store's cause", err)
			}
			if _, ok := g.getCachedUser(token); ok {
				t.Error("Logout left the cached user in place")
			}
			store.failWrite.Store(false)
			if _, err := g.RefreshToken(refresh); !errors.Is(err, auth.ErrRefreshGenerationStale) {
				t.Errorf("RefreshToken after the failed Logout = %v, want ErrRefreshGenerationStale: the generation was not bumped", err)
			}
		})
	}
}

// A blacklist store that is down for reads and writes: Logout still ends
// the user's refresh tokens and drops the cached user, and returns the
// outage. The sign-out does not depend on reading the store it writes.
func TestJWTScheme_Logout_BlacklistDown_StillBumpsGenerationAndReports(t *testing.T) {
	for _, panics := range []bool{false, true} {
		name := "error"
		if panics {
			name = "panic"
		}
		t.Run(name, func(t *testing.T) {
			store := &outageBlacklist{InMemoryBlacklistStore: auth.NewInMemoryBlacklistStore()}
			g, r, _ := outageScheme(t, store)
			refresh, err := g.GenerateRefreshToken(&mockJWTUser{id: "user123"})
			if err != nil {
				t.Fatalf("GenerateRefreshToken: %v", err)
			}
			if g.User(r) == nil {
				t.Fatal("test setup error: the token does not authenticate")
			}
			token := g.getTokenFromRequest(r)

			store.panics.Store(panics)
			store.failRead.Store(true)
			store.failWrite.Store(true)
			err = g.Logout(httptest.NewRecorder(), r)
			if !errors.Is(err, auth.ErrBlacklistUnavailable) || !errors.Is(err, errOutageStore) {
				t.Fatalf("Logout error = %v, want ErrBlacklistUnavailable wrapping the store's cause", err)
			}
			if _, ok := g.getCachedUser(token); ok {
				t.Error("Logout left the cached user in place")
			}

			store.failRead.Store(false)
			store.failWrite.Store(false)
			if _, err := g.RefreshToken(refresh); !errors.Is(err, auth.ErrRefreshGenerationStale) {
				t.Errorf("RefreshToken after the Logout during the outage = %v, want ErrRefreshGenerationStale: a refresh token issued before still mints", err)
			}
		})
	}
}

// A blacklist store that cannot be read but takes writes: the sign-out
// lands in full, with no error.
func TestJWTScheme_Logout_BlacklistReadDown_Revokes(t *testing.T) {
	store := &outageBlacklist{InMemoryBlacklistStore: auth.NewInMemoryBlacklistStore()}
	g, r, _ := outageScheme(t, store)
	refresh, err := g.GenerateRefreshToken(&mockJWTUser{id: "user123"})
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	store.failRead.Store(true)
	if err := g.Logout(httptest.NewRecorder(), r); err != nil {
		t.Fatalf("Logout with only the blacklist read down = %v, want nil", err)
	}
	store.failRead.Store(false)
	if g.Check(r) {
		t.Error("the access token still authenticates after Logout")
	}
	if _, err := g.RefreshToken(refresh); !errors.Is(err, auth.ErrRefreshGenerationStale) {
		t.Errorf("RefreshToken after Logout = %v, want ErrRefreshGenerationStale", err)
	}
}

// parkedBlacklist parks every Add until released.
type parkedBlacklist struct {
	*auth.InMemoryBlacklistStore
	entered chan struct{}
	release chan struct{}
}

func (s *parkedBlacklist) Add(jti string, expiresAt time.Time) (bool, error) {
	s.entered <- struct{}{}
	<-s.release
	return s.InMemoryBlacklistStore.Add(jti, expiresAt)
}

// A store whose write is parked: Logout waits for it holding no lock of
// the scheme or the manager (another request is served and the store is
// swapped meanwhile), and completes when the store answers.
func TestJWTScheme_Logout_BlockingStore_HoldsNoLock(t *testing.T) {
	store := &parkedBlacklist{
		InMemoryBlacklistStore: auth.NewInMemoryBlacklistStore(),
		entered:                make(chan struct{}, 1),
		release:                make(chan struct{}),
	}
	g, r, _ := outageScheme(t, store)
	refresh, err := g.GenerateRefreshToken(&mockJWTUser{id: "user123"})
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- g.Logout(httptest.NewRecorder(), r) }()
	<-store.entered
	select {
	case err := <-done:
		t.Fatalf("Logout returned while the store's Add was parked: %v", err)
	default:
	}

	// Reads take the scheme's cache lock and the manager's store lock;
	// the swap takes the manager's write lock. All return while Logout is
	// parked in the store.
	served := make(chan struct{})
	go func() {
		g.User(r)
		g.jwtManager.SetBlacklistStore(store)
		close(served)
	}()
	<-served

	close(store.release)
	if err := <-done; err != nil {
		t.Fatalf("Logout after the store answered: %v", err)
	}
	if g.Check(r) {
		t.Error("the access token still authenticates after Logout")
	}
	if _, err := g.RefreshToken(refresh); !errors.Is(err, auth.ErrRefreshGenerationStale) {
		t.Errorf("RefreshToken after Logout = %v, want ErrRefreshGenerationStale", err)
	}
}

// Logout signs out with either token type: a refresh token presented to
// it is revoked and the user's refresh generation is bumped.
func TestJWTScheme_Logout_RefreshToken_RevokesAndBumps(t *testing.T) {
	store := &outageBlacklist{InMemoryBlacklistStore: auth.NewInMemoryBlacklistStore()}
	g, _, _ := outageScheme(t, store)
	user := &mockJWTUser{id: "user123"}
	refresh, err := g.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	other, err := g.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+refresh)
	if err := g.Logout(httptest.NewRecorder(), r); err != nil {
		t.Fatalf("Logout with a refresh token = %v, want nil", err)
	}
	if _, err := g.RefreshToken(refresh); !errors.Is(err, auth.ErrRefreshTokenUsed) {
		t.Errorf("RefreshToken of the refresh token Logout was given = %v, want ErrRefreshTokenUsed: it was not revoked", err)
	}
	if _, err := g.RefreshToken(other); !errors.Is(err, auth.ErrRefreshGenerationStale) {
		t.Errorf("RefreshToken of the user's other refresh token = %v, want ErrRefreshGenerationStale: the generation was not bumped", err)
	}
}

// A token that does not verify is already signed out: Logout returns nil
// and changes nothing.
func TestJWTScheme_Logout_ForgedToken_ChangesNothing(t *testing.T) {
	store := &outageBlacklist{InMemoryBlacklistStore: auth.NewInMemoryBlacklistStore()}
	g, _, _ := outageScheme(t, store)
	user := &mockJWTUser{id: "user123"}
	refresh, err := g.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	forgerCfg := newTestJWTConfig()
	forgerCfg.Secret = "another-secret-key-for-jwt-signing-minimum-length"
	forged, err := mustNewJWTScheme(&mockJWTUserStore{}, forgerCfg).GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	for name, token := range map[string]string{"forged": forged, "malformed": "not.a.token"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		if err := g.Logout(httptest.NewRecorder(), r); err != nil {
			t.Errorf("%s: Logout = %v, want nil", name, err)
		}
	}
	if _, err := g.RefreshToken(refresh); err != nil {
		t.Fatalf("RefreshToken after Logout with tokens that do not verify = %v: something was revoked or bumped", err)
	}
}

// The scheme keeps no reference to the blacklist store of its own: the
// manager's contained calls are the only way to it.
func TestJWTScheme_KeepsNoStoreReference(t *testing.T) {
	store := &outageBlacklist{InMemoryBlacklistStore: auth.NewInMemoryBlacklistStore()}
	g, r, _ := outageScheme(t, store)
	if g.config.BlacklistStore != nil {
		t.Fatal("the scheme's config copy still holds the blacklist store")
	}
	if !g.Check(r) {
		t.Fatal("the scheme does not authenticate with the configured store")
	}
}

// A refresh token from before a generation bump presented to Logout is
// already signed out: Logout returns nil, bumps nothing and blacklists
// nothing.
func TestJWTScheme_Logout_StaleRefreshToken_ChangesNothing(t *testing.T) {
	store := &outageBlacklist{InMemoryBlacklistStore: auth.NewInMemoryBlacklistStore()}
	g, _, _ := outageScheme(t, store)
	user := &mockJWTUser{id: "user123"}
	stale, err := g.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	staleClaims, err := g.ValidateToken(stale)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if err := g.RevokeAllRefreshTokensForUser(t.Context(), "user123"); err != nil {
		t.Fatalf("RevokeAllRefreshTokensForUser: %v", err)
	}
	current, err := g.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+stale)
	if err := g.Logout(httptest.NewRecorder(), r); err != nil {
		t.Fatalf("Logout with a stale refresh token = %v, want nil", err)
	}
	if listed, err := store.InMemoryBlacklistStore.IsBlacklisted(staleClaims.ID); listed || err != nil {
		t.Errorf("the stale refresh token's id was blacklisted (%v, %v)", listed, err)
	}
	if _, err := g.RefreshToken(current); err != nil {
		t.Fatalf("RefreshToken of the user's current refresh token = %v: the stale token ended it", err)
	}
}

var errOutageGeneration = errors.New("generation backend down")

// downGenerations fails every read.
type downGenerations struct {
	*auth.InMemoryRefreshGenerationStore
}

func (downGenerations) Current(string) (int64, error) { return 0, errOutageGeneration }

// A generation store that cannot answer the stale check is an error at
// Logout, not a silent "already signed out", and nothing is written.
func TestJWTScheme_Logout_RefreshToken_GenerationCheckFails_ReturnsError(t *testing.T) {
	store := &outageBlacklist{InMemoryBlacklistStore: auth.NewInMemoryBlacklistStore()}
	g, _, _ := outageScheme(t, store)
	refresh, err := g.GenerateRefreshToken(&mockJWTUser{id: "user123"})
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	claims, err := g.ValidateToken(refresh)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	g.SetRefreshGenerationStore(downGenerations{auth.NewInMemoryRefreshGenerationStore()})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+refresh)
	if err := g.Logout(httptest.NewRecorder(), r); err == nil {
		t.Fatal("Logout returned nil though the generation store could not answer the check")
	}
	if listed, err := store.InMemoryBlacklistStore.IsBlacklisted(claims.ID); listed || err != nil {
		t.Errorf("the refresh token's id was blacklisted though the check failed (%v, %v)", listed, err)
	}
}
