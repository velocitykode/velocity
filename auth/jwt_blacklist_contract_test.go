package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/velocitykode/velocity/contract"
)

// Blacklist store contract: every store call fails closed on an error or a
// panic, under no manager lock, and the manager and its bundled store read
// one clock.

const (
	hostilePass int32 = iota
	hostileError
	hostilePanic
	hostileBlock
	hostileLie // answers the permissive value together with an error
)

var errHostileBlacklist = errors.New("blacklist backend down")

// hostileBlacklist is a store whose three methods each pass through to an
// in-memory store, return an error, panic, park until released, or return
// the permissive answer beside an error, per its mode.
type hostileBlacklist struct {
	inner   *InMemoryBlacklistStore
	mode    atomic.Int32
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func newHostileBlacklist() *hostileBlacklist {
	return &hostileBlacklist{
		inner:   NewInMemoryBlacklistStore(),
		entered: make(chan struct{}, 64),
		release: make(chan struct{}),
	}
}

// act runs the mode's side effect and reports the error to return, if any.
func (s *hostileBlacklist) act() error {
	s.calls.Add(1)
	switch s.mode.Load() {
	case hostileError, hostileLie:
		return errHostileBlacklist
	case hostilePanic:
		panic(errHostileBlacklist)
	case hostileBlock:
		s.entered <- struct{}{}
		<-s.release
	}
	return nil
}

func (s *hostileBlacklist) Add(jti string, expiresAt time.Time) (bool, error) {
	if err := s.act(); err != nil {
		return s.mode.Load() == hostileLie, err
	}
	return s.inner.Add(jti, expiresAt)
}

func (s *hostileBlacklist) IsBlacklisted(jti string) (bool, error) {
	if err := s.act(); err != nil {
		return false, err
	}
	return s.inner.IsBlacklisted(jti)
}

func (s *hostileBlacklist) Cleanup() error {
	if err := s.act(); err != nil {
		return err
	}
	return s.inner.Cleanup()
}

var failingModes = []struct {
	name string
	mode int32
}{
	{"error", hostileError},
	{"panic", hostilePanic},
	{"permissive answer beside an error", hostileLie},
}

// assertUnavailable checks err is the outage sentinel, keeps the store's
// cause, and is not one of the answers a working store gives.
func assertUnavailable(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, ErrBlacklistUnavailable) {
		t.Fatalf("%s error = %v, want it to wrap ErrBlacklistUnavailable", what, err)
	}
	if !errors.Is(err, errHostileBlacklist) {
		t.Errorf("%s error = %v, want it to keep the store's cause", what, err)
	}
	if errors.Is(err, ErrRefreshTokenUsed) || errors.Is(err, errTokenRevoked) || errors.Is(err, jwt.ErrTokenExpired) {
		t.Errorf("%s error = %v: an outage must not be reported as used, revoked or expired", what, err)
	}
}

func TestJWT_ValidateToken_StoreFailure_Refuses(t *testing.T) {
	for _, tc := range failingModes {
		t.Run(tc.name, func(t *testing.T) {
			store := newHostileBlacklist()
			mgr := newConsumeManager(t, store, true)
			token, err := mgr.GenerateToken(&jwtRefreshTestUser{id: "user-1"})
			if err != nil {
				t.Fatalf("GenerateToken: %v", err)
			}
			store.mode.Store(tc.mode)

			claims, err := mgr.ValidateToken(token)
			if claims != nil {
				t.Fatalf("ValidateToken returned claims from a store that could not answer: %+v", claims)
			}
			assertUnavailable(t, "ValidateToken", err)

			claims, err = mgr.ValidateAccessToken(token)
			if claims != nil {
				t.Fatalf("ValidateAccessToken returned claims from a store that could not answer: %+v", claims)
			}
			assertUnavailable(t, "ValidateAccessToken", err)

			listed, err := mgr.IsBlacklisted("any")
			if !listed {
				t.Error("IsBlacklisted = false beside an error: a caller that drops the error would accept the token")
			}
			assertUnavailable(t, "IsBlacklisted", err)

			// The store recovers: the same token validates again.
			store.mode.Store(hostilePass)
			if _, err := mgr.ValidateToken(token); err != nil {
				t.Fatalf("ValidateToken after the store recovered: %v", err)
			}
		})
	}
}

func TestJWT_RevokeToken_StoreFailure_ReturnsError(t *testing.T) {
	for _, tc := range failingModes {
		t.Run(tc.name, func(t *testing.T) {
			store := newHostileBlacklist()
			mgr := newConsumeManager(t, store, true)
			store.mode.Store(tc.mode)
			err := mgr.RevokeToken("jti-1", time.Now().Add(time.Hour))
			assertUnavailable(t, "RevokeToken", err)
			if yes(store.inner.IsBlacklisted("jti-1")) {
				t.Fatal("test setup error: the failing store wrote the entry")
			}
		})
	}
}

func TestJWT_RevokeToken_BlacklistDisabled_NoStoreCall(t *testing.T) {
	store := newHostileBlacklist()
	store.mode.Store(hostilePanic)
	mgr := newConsumeManager(t, store, false)
	if err := mgr.RevokeToken("jti-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("RevokeToken with the blacklist disabled = %v, want nil", err)
	}
	if listed, err := mgr.IsBlacklisted("jti-1"); listed || err != nil {
		t.Fatalf("IsBlacklisted with the blacklist disabled = (%v, %v), want (false, nil)", listed, err)
	}
	if n := store.calls.Load(); n != 0 {
		t.Fatalf("store called %d times with the blacklist disabled", n)
	}
}

func TestJWT_CleanupBlacklist_StoreFailure_ReturnsError(t *testing.T) {
	for _, tc := range failingModes {
		t.Run(tc.name, func(t *testing.T) {
			store := newHostileBlacklist()
			mgr := newConsumeManager(t, store, true)
			store.mode.Store(tc.mode)
			assertUnavailable(t, "CleanupBlacklist", mgr.CleanupBlacklist())
			store.mode.Store(hostilePass)
			if err := mgr.CleanupBlacklist(); err != nil {
				t.Fatalf("CleanupBlacklist after the store recovered: %v", err)
			}
		})
	}
}

// A store that cannot answer the read at the start of a refresh: nothing
// is issued, the user store is not asked, and the outage keeps its own
// error.
func TestJWT_RefreshToken_ReadFailure_IssuesNothing(t *testing.T) {
	for _, tc := range failingModes {
		t.Run(tc.name, func(t *testing.T) {
			user := &jwtRefreshTestUser{id: "user-1"}
			users := &jwtRefreshTestStore{user: user}
			store := newHostileBlacklist()
			mgr := newConsumeManager(t, store, true)
			refresh, err := mgr.GenerateRefreshToken(user)
			if err != nil {
				t.Fatalf("GenerateRefreshToken: %v", err)
			}
			store.mode.Store(tc.mode)

			token, err := mgr.RefreshToken(refresh, users)
			if token != "" {
				t.Fatal("RefreshToken issued a token while the store could not answer")
			}
			assertUnavailable(t, "RefreshToken", err)
			if users.findByIDCalls != 0 {
				t.Errorf("user store asked %d times for a token that was refused at validation", users.findByIDCalls)
			}

			// Nothing was spent: the store recovers and the token refreshes.
			store.mode.Store(hostilePass)
			if _, err := mgr.RefreshToken(refresh, users); err != nil {
				t.Fatalf("RefreshToken after the store recovered: %v", err)
			}
		})
	}
}

// failAddBlacklist answers reads from the in-memory store and fails Add in
// the configured way.
type failAddBlacklist struct {
	*InMemoryBlacklistStore
	mode int32
}

func (s *failAddBlacklist) Add(string, time.Time) (bool, error) {
	switch s.mode {
	case hostilePanic:
		panic(errHostileBlacklist)
	case hostileLie:
		return true, errHostileBlacklist
	}
	return false, errHostileBlacklist
}

// A store that cannot take the consume: nothing is issued, and the outage
// is not reported as "already used".
func TestJWT_RefreshToken_ConsumeFailure_IssuesNothing(t *testing.T) {
	for _, tc := range failingModes {
		t.Run(tc.name, func(t *testing.T) {
			user := &jwtRefreshTestUser{id: "user-1"}
			users := &consumeUserStore{user: user}
			store := &failAddBlacklist{InMemoryBlacklistStore: NewInMemoryBlacklistStore(), mode: tc.mode}
			mgr := newConsumeManager(t, store, true)
			refresh, err := mgr.GenerateRefreshToken(user)
			if err != nil {
				t.Fatalf("GenerateRefreshToken: %v", err)
			}
			token, err := mgr.RefreshToken(refresh, users)
			if token != "" {
				t.Fatal("RefreshToken issued a token though the consume failed")
			}
			assertUnavailable(t, "RefreshToken", err)
		})
	}
}

// A store whose read is parked: the manager holds no lock across the call
// (a store swap goes through meanwhile), and the answer the parked call
// finally gives is the store's.
func TestJWT_ValidateToken_BlockingStore_NoManagerLockHeld(t *testing.T) {
	store := newHostileBlacklist()
	mgr := newConsumeManager(t, store, true)
	token, err := mgr.GenerateToken(&jwtRefreshTestUser{id: "user-1"})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	store.mode.Store(hostileBlock)

	type result struct {
		claims *Claims
		err    error
	}
	done := make(chan result, 1)
	go func() {
		claims, err := mgr.ValidateToken(token)
		done <- result{claims, err}
	}()
	<-store.entered
	select {
	case r := <-done:
		t.Fatalf("ValidateToken returned while the store's read was parked: %v", r.err)
	default:
	}

	// The swap takes the manager's write lock: it returns only if the
	// parked caller holds no read lock across the store call.
	swapped := make(chan struct{})
	go func() {
		mgr.SetBlacklistStore(store)
		close(swapped)
	}()
	<-swapped

	store.mode.Store(hostilePass)
	close(store.release)
	r := <-done
	if r.err != nil || r.claims == nil {
		t.Fatalf("ValidateToken after the store answered = (%v, %v), want claims", r.claims, r.err)
	}
}

// A store whose revoke and cleanup are parked: same, no manager lock held.
func TestJWT_RevokeAndCleanup_BlockingStore_NoManagerLockHeld(t *testing.T) {
	calls := map[string]func(*JWTManager) error{
		"RevokeToken":      func(m *JWTManager) error { return m.RevokeToken("jti-1", time.Now().Add(time.Hour)) },
		"CleanupBlacklist": func(m *JWTManager) error { return m.CleanupBlacklist() },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			store := newHostileBlacklist()
			mgr := newConsumeManager(t, store, true)
			store.mode.Store(hostileBlock)
			done := make(chan error, 1)
			go func() { done <- call(mgr) }()
			<-store.entered

			swapped := make(chan struct{})
			go func() {
				mgr.SetBlacklistStore(store)
				close(swapped)
			}()
			<-swapped

			store.mode.Store(hostilePass)
			close(store.release)
			if err := <-done; err != nil {
				t.Fatalf("%s after the store answered: %v", name, err)
			}
		})
	}
}

// steppedClock is a clock a test moves by hand.
type steppedClock struct {
	mu  sync.Mutex
	now time.Time
}

func newSteppedClock() *steppedClock {
	// Whole seconds: JWT numeric dates carry no finer grain.
	return &steppedClock{now: time.Now().Truncate(time.Second)}
}

func (c *steppedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *steppedClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// newClockedManager builds a manager on clock. With store nil it runs on
// the in-memory store the manager builds itself, which reads the same
// clock.
func newClockedManager(t *testing.T, clock *steppedClock, store BlacklistStore, enabled bool) *JWTManager {
	t.Helper()
	initial := store
	if initial == nil {
		initial = NewInMemoryBlacklistStore()
	}
	mgr := newConsumeManager(t, initial, enabled)
	mgr.now = clock.Now
	if store == nil {
		mgr.SetBlacklistStore(nil)
	}
	return mgr
}

// The manager's clock is the one clock: minting, the parser's expiry check
// and the bundled store's entries all move with it, with no sleep.
func TestJWT_OneClock_ManagerParserAndBundledStore(t *testing.T) {
	clock := newSteppedClock()
	mgr := newClockedManager(t, clock, nil, true)
	user := &jwtRefreshTestUser{id: "user-1"}

	token, err := mgr.GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	claims, err := mgr.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if !claims.IssuedAt.Time.Equal(clock.Now()) {
		t.Fatalf("iat = %v, want the manager clock's %v", claims.IssuedAt.Time, clock.Now())
	}
	if want := clock.Now().Add(60 * time.Minute); !claims.ExpiresAt.Time.Equal(want) {
		t.Fatalf("exp = %v, want %v", claims.ExpiresAt.Time, want)
	}

	// A revocation without an expiry lasts the access TTL on the manager's
	// clock, and the bundled store judges it on that clock too.
	if err := mgr.RevokeToken("ttl-entry"); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	clock.Advance(59 * time.Minute)
	if !yes(mgr.IsBlacklisted("ttl-entry")) {
		t.Fatal("entry gone before its expiry on the manager's clock")
	}
	if _, err := mgr.ValidateToken(token); err != nil {
		t.Fatalf("ValidateToken one minute before expiry: %v", err)
	}

	clock.Advance(2 * time.Minute)
	if yes(mgr.IsBlacklisted("ttl-entry")) {
		t.Fatal("entry still live after its expiry on the manager's clock: the store reads another clock")
	}
	if _, err := mgr.ValidateToken(token); !errors.Is(err, jwt.ErrTokenExpired) {
		t.Fatalf("ValidateToken after expiry on the manager's clock = %v, want jwt.ErrTokenExpired: the parser reads another clock", err)
	}
}

// expiringReadBlacklist moves the clock past a deadline during the read,
// standing for a read that blocks while the token expires.
type expiringReadBlacklist struct {
	*InMemoryBlacklistStore
	clock *steppedClock
	jump  time.Duration
}

func (s *expiringReadBlacklist) IsBlacklisted(jti string) (bool, error) {
	s.clock.Advance(s.jump)
	return s.InMemoryBlacklistStore.IsBlacklisted(jti)
}

// A token that expires while the store's read is pending is refused: the
// expiry is judged again on a fresh reading after the store call.
func TestJWT_ValidateToken_ExpiresDuringStoreRead_Refused(t *testing.T) {
	clock := newSteppedClock()
	store := &expiringReadBlacklist{InMemoryBlacklistStore: newInMemoryBlacklistStore(clock.Now), clock: clock}
	mgr := newClockedManager(t, clock, store, true)
	token, err := mgr.GenerateToken(&jwtRefreshTestUser{id: "user-1"})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if _, err := mgr.ValidateToken(token); err != nil {
		t.Fatalf("ValidateToken with a prompt store: %v", err)
	}

	store.jump = 61 * time.Minute
	claims, err := mgr.ValidateToken(token)
	if claims != nil || !errors.Is(err, jwt.ErrTokenExpired) {
		t.Fatalf("ValidateToken = (%v, %v), want (nil, jwt.ErrTokenExpired) for a token that expired during the store read", claims, err)
	}
}

// hookedUserStore runs a hook inside FindByID, standing for a lookup that
// takes long enough for the world to change.
type hookedUserStore struct {
	user contract.Authenticatable
	hook func()
}

func (p *hookedUserStore) FindByID(interface{}) (contract.Authenticatable, error) {
	if p.hook != nil {
		p.hook()
	}
	return p.user, nil
}
func (p *hookedUserStore) FindByIDCtx(_ context.Context, id interface{}) (contract.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *hookedUserStore) FindByCredentials(map[string]interface{}) (contract.Authenticatable, error) {
	return p.user, nil
}
func (p *hookedUserStore) FindByCredentialsCtx(context.Context, map[string]interface{}) (contract.Authenticatable, error) {
	return p.user, nil
}
func (p *hookedUserStore) ValidateCredentials(contract.Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *hookedUserStore) ValidateCredentialsCtx(context.Context, contract.Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *hookedUserStore) UpdateRememberToken(contract.Authenticatable, string) error { return nil }
func (p *hookedUserStore) UpdateRememberTokenCtx(context.Context, contract.Authenticatable, string) error {
	return nil
}

// hookedIdentityUser runs a hook when its identifier is read.
type hookedIdentityUser struct {
	id   string
	hook func()
}

func (u *hookedIdentityUser) GetAuthIdentifier() interface{} {
	if u.hook != nil {
		u.hook()
	}
	return u.id
}
func (u *hookedIdentityUser) GetAuthPassword() string  { return "" }
func (u *hookedIdentityUser) GetRememberToken() string { return "" }
func (u *hookedIdentityUser) SetRememberToken(string)  {}

var blacklistModes = []struct {
	name    string
	enabled bool
}{{"blacklist on", true}, {"blacklist off", false}}

// A refresh token that expires during the user lookup mints nothing, with
// the blacklist on or off.
func TestJWT_RefreshToken_ExpiresDuringUserLookup_IssuesNothing(t *testing.T) {
	for _, mode := range blacklistModes {
		t.Run(mode.name, func(t *testing.T) {
			clock := newSteppedClock()
			mgr := newClockedManager(t, clock, nil, mode.enabled)
			user := &jwtRefreshTestUser{id: "user-1"}
			refresh, err := mgr.GenerateRefreshToken(user)
			if err != nil {
				t.Fatalf("GenerateRefreshToken: %v", err)
			}
			users := &hookedUserStore{user: user, hook: func() { clock.Advance(20161 * time.Minute) }}

			token, err := mgr.RefreshToken(refresh, users)
			if token != "" {
				t.Fatal("RefreshToken minted an access token for a refresh token that expired during the user lookup")
			}
			if !errors.Is(err, jwt.ErrTokenExpired) {
				t.Fatalf("RefreshToken error = %v, want jwt.ErrTokenExpired", err)
			}
		})
	}
}

// A sign-out that bumps the user's refresh generation during the user
// lookup refuses the refresh, with the blacklist on or off, and does not
// spend the token.
func TestJWT_RefreshToken_GenerationBumpedDuringUserLookup_IssuesNothing(t *testing.T) {
	for _, mode := range blacklistModes {
		t.Run(mode.name, func(t *testing.T) {
			store := NewInMemoryBlacklistStore()
			mgr := newConsumeManager(t, store, mode.enabled)
			user := &jwtRefreshTestUser{id: "user-1"}
			refresh, err := mgr.GenerateRefreshToken(user)
			if err != nil {
				t.Fatalf("GenerateRefreshToken: %v", err)
			}
			claims, err := mgr.ValidateToken(refresh)
			if err != nil {
				t.Fatalf("ValidateToken: %v", err)
			}
			users := &hookedUserStore{user: user, hook: func() {
				if _, err := mgr.BumpRefreshGeneration("user-1"); err != nil {
					t.Errorf("BumpRefreshGeneration: %v", err)
				}
			}}

			token, err := mgr.RefreshToken(refresh, users)
			if token != "" {
				t.Fatal("RefreshToken minted an access token after the generation was bumped during the user lookup")
			}
			if !errors.Is(err, ErrRefreshGenerationStale) {
				t.Fatalf("RefreshToken error = %v, want ErrRefreshGenerationStale", err)
			}
			if yes(store.IsBlacklisted(claims.ID)) {
				t.Error("the refused refresh spent the token")
			}
		})
	}
}

// The identity the new token is signed for is read before the final
// decision: a sign-out that lands while the user value answers refuses the
// refresh, and the user value is read once.
func TestJWT_RefreshToken_GenerationBumpedDuringIdentityRead_IssuesNothing(t *testing.T) {
	for _, mode := range blacklistModes {
		t.Run(mode.name, func(t *testing.T) {
			store := NewInMemoryBlacklistStore()
			mgr := newConsumeManager(t, store, mode.enabled)
			refresh, err := mgr.GenerateRefreshToken(&jwtRefreshTestUser{id: "user-1"})
			if err != nil {
				t.Fatalf("GenerateRefreshToken: %v", err)
			}
			var reads atomic.Int32
			user := &hookedIdentityUser{id: "user-1", hook: func() {
				reads.Add(1)
				if _, err := mgr.BumpRefreshGeneration("user-1"); err != nil {
					t.Errorf("BumpRefreshGeneration: %v", err)
				}
			}}

			token, err := mgr.RefreshToken(refresh, &hookedUserStore{user: user})
			if token != "" {
				t.Fatal("RefreshToken minted an access token after the generation was bumped during the identity read")
			}
			if !errors.Is(err, ErrRefreshGenerationStale) {
				t.Fatalf("RefreshToken error = %v, want ErrRefreshGenerationStale", err)
			}
			if n := reads.Load(); n != 1 {
				t.Errorf("user identity read %d times, want 1", n)
			}
		})
	}
}

// The access token is signed from the identity snapshot: a successful
// refresh reads the user value once and the token names that identity.
func TestJWT_RefreshToken_SignsFromIdentitySnapshot(t *testing.T) {
	mgr := newConsumeManager(t, NewInMemoryBlacklistStore(), true)
	refresh, err := mgr.GenerateRefreshToken(&jwtRefreshTestUser{id: "user-1"})
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	var reads atomic.Int32
	user := &hookedIdentityUser{id: "user-1", hook: func() { reads.Add(1) }}
	token, err := mgr.RefreshToken(refresh, &hookedUserStore{user: user})
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("user identity read %d times, want 1", n)
	}
	claims, err := mgr.ValidateAccessToken(token)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if claims.UserID != "user-1" || claims.Subject != "user-1" {
		t.Errorf("issued token names (%v, %q), want user-1", claims.UserID, claims.Subject)
	}
}

// N callers validate, revoke, clean up and swap the store while the store
// moves between passing, failing and panicking: no data race, no panic
// out of the manager, and every refusal is the outage or a plain verdict.
func TestJWT_BlacklistStore_HostileConcurrent(t *testing.T) {
	const callers = 16
	store := newHostileBlacklist()
	mgr := newConsumeManager(t, store, true)
	token, err := mgr.GenerateToken(&jwtRefreshTestUser{id: "user-1"})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	modes := []int32{hostilePass, hostileError, hostilePanic, hostileLie}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for n := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range 200 {
				store.mode.Store(modes[(n+i)%len(modes)])
				if _, err := mgr.ValidateToken(token); err != nil && !errors.Is(err, ErrBlacklistUnavailable) {
					t.Errorf("ValidateToken: unexpected error %v", err)
				}
				jti := "jti-" + strings.Repeat("x", n%4)
				if err := mgr.RevokeToken(jti, time.Now().Add(time.Hour)); err != nil && !errors.Is(err, ErrBlacklistUnavailable) {
					t.Errorf("RevokeToken: unexpected error %v", err)
				}
				if listed, err := mgr.IsBlacklisted(jti); err != nil && !listed {
					t.Errorf("IsBlacklisted = (false, %v): an error must come with true", err)
				}
				if err := mgr.CleanupBlacklist(); err != nil && !errors.Is(err, ErrBlacklistUnavailable) {
					t.Errorf("CleanupBlacklist: unexpected error %v", err)
				}
				if i%50 == 0 {
					mgr.SetBlacklistStore(store)
				}
			}
		}()
	}
	close(start)
	wg.Wait()
}

// failingGenerationStore fails every Bump.
type failingGenerationStore struct {
	*InMemoryRefreshGenerationStore
}

var errHostileGeneration = errors.New("generation backend down")

func (s *failingGenerationStore) Bump(string) (int64, error) { return 0, errHostileGeneration }

// SignOutToken verifies the token without the blacklist read, then
// attempts both the revocation and the generation bump whatever the first
// one did, and returns what failed.
func TestJWT_SignOutToken_StoreFailure_StillBumpsGeneration(t *testing.T) {
	for _, tc := range failingModes {
		t.Run(tc.name, func(t *testing.T) {
			store := newHostileBlacklist()
			mgr := newConsumeManager(t, store, true)
			user := &jwtRefreshTestUser{id: "user-1"}
			token, err := mgr.GenerateToken(user)
			if err != nil {
				t.Fatalf("GenerateToken: %v", err)
			}
			store.mode.Store(tc.mode)

			claims, err := mgr.SignOutToken(token)
			if claims == nil || claims.UserID != "user-1" {
				t.Fatalf("SignOutToken claims = %+v, want the verified token's", claims)
			}
			assertUnavailable(t, "SignOutToken", err)
			if gen, _ := mgr.CurrentRefreshGeneration("user-1"); gen != 1 {
				t.Errorf("refresh generation = %d after the failed revocation, want 1", gen)
			}
		})
	}
}

func TestJWT_SignOutToken_RevokesAndBumps(t *testing.T) {
	store := NewInMemoryBlacklistStore()
	mgr := newConsumeManager(t, store, true)
	token, err := mgr.GenerateToken(&jwtRefreshTestUser{id: "user-1"})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	claims, err := mgr.SignOutToken(token)
	if err != nil || claims == nil {
		t.Fatalf("SignOutToken = (%v, %v), want claims and nil", claims, err)
	}
	if !yes(store.IsBlacklisted(claims.ID)) {
		t.Error("the token id is not on the blacklist")
	}
	if gen, _ := mgr.CurrentRefreshGeneration("user-1"); gen != 1 {
		t.Errorf("refresh generation = %d, want 1", gen)
	}
	// A second sign-out of the same token succeeds and, by the ruled order,
	// bumps again: the bump never depends on the blacklist's answer.
	if _, err := mgr.SignOutToken(token); err != nil {
		t.Fatalf("second SignOutToken = %v, want nil", err)
	}
	if gen, _ := mgr.CurrentRefreshGeneration("user-1"); gen != 2 {
		t.Errorf("refresh generation = %d after the second sign-out, want 2", gen)
	}
}

// Both failures are reported: the blacklist store's and the generation
// store's.
func TestJWT_SignOutToken_BothStoresFail_ReportsBoth(t *testing.T) {
	store := newHostileBlacklist()
	mgr := newConsumeManager(t, store, true)
	token, err := mgr.GenerateToken(&jwtRefreshTestUser{id: "user-1"})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	mgr.SetRefreshGenerationStore(&failingGenerationStore{NewInMemoryRefreshGenerationStore()})
	store.mode.Store(hostileError)

	claims, err := mgr.SignOutToken(token)
	if claims == nil {
		t.Fatal("SignOutToken returned no claims for a verified token")
	}
	if !errors.Is(err, ErrBlacklistUnavailable) || !errors.Is(err, errHostileGeneration) {
		t.Fatalf("SignOutToken error = %v, want both the blacklist outage and the generation store's error", err)
	}

	store.mode.Store(hostilePass)
	if _, err := mgr.SignOutToken(token); !errors.Is(err, errHostileGeneration) || errors.Is(err, ErrBlacklistUnavailable) {
		t.Fatalf("SignOutToken with only the generation store down = %v, want its error alone", err)
	}
}

// With the blacklist disabled the sign-out still ends the user's refresh
// tokens and never calls the store.
func TestJWT_SignOutToken_BlacklistDisabled_BumpsOnly(t *testing.T) {
	store := newHostileBlacklist()
	store.mode.Store(hostilePanic)
	mgr := newConsumeManager(t, store, false)
	token, err := mgr.GenerateToken(&jwtRefreshTestUser{id: "user-1"})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if _, err := mgr.SignOutToken(token); err != nil {
		t.Fatalf("SignOutToken = %v, want nil", err)
	}
	if n := store.calls.Load(); n != 0 {
		t.Errorf("store called %d times with the blacklist disabled", n)
	}
	if gen, _ := mgr.CurrentRefreshGeneration("user-1"); gen != 1 {
		t.Errorf("refresh generation = %d, want 1", gen)
	}
}

// A refresh token signs the user out as an access token does: its id goes
// on the blacklist and the generation is bumped.
func TestJWT_SignOutToken_RefreshToken_RevokesAndBumps(t *testing.T) {
	store := NewInMemoryBlacklistStore()
	mgr := newConsumeManager(t, store, true)
	user := &jwtRefreshTestUser{id: "user-1"}
	refresh, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	other, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	claims, err := mgr.SignOutToken(refresh)
	if err != nil || claims == nil || claims.TokenType != "refresh" {
		t.Fatalf("SignOutToken(refresh) = (%+v, %v), want the refresh token's claims and nil", claims, err)
	}
	if !yes(store.IsBlacklisted(claims.ID)) {
		t.Error("the refresh token's id is not on the blacklist")
	}
	users := &jwtRefreshTestStore{user: user}
	if _, err := mgr.RefreshToken(refresh, users); !errors.Is(err, ErrRefreshTokenUsed) {
		t.Errorf("RefreshToken of the signed-out refresh token = %v, want ErrRefreshTokenUsed", err)
	}
	if _, err := mgr.RefreshToken(other, users); !errors.Is(err, ErrRefreshGenerationStale) {
		t.Errorf("RefreshToken of the user's other refresh token = %v, want ErrRefreshGenerationStale", err)
	}
}

// A token that does not verify changes nothing: forged, malformed, empty
// or expired, it reaches neither store.
func TestJWT_SignOutToken_UnverifiedToken_ChangesNothing(t *testing.T) {
	clock := newSteppedClock()
	store := newHostileBlacklist()
	mgr := newClockedManager(t, clock, store, true)
	user := &jwtRefreshTestUser{id: "user-1"}
	refresh, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	expired, err := mgr.GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	clock.Advance(61 * time.Minute) // past the access TTL, inside the refresh TTL

	other := newConsumeManager(t, NewInMemoryBlacklistStore(), true)
	other.config.Secret = strings.Repeat("o", 64)
	forged, err := other.GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	for name, token := range map[string]string{"expired": expired, "forged": forged, "malformed": "not.a.token", "empty": ""} {
		claims, err := mgr.SignOutToken(token)
		if claims != nil || err == nil {
			t.Errorf("%s: SignOutToken = (%v, %v), want (nil, error)", name, claims, err)
		}
	}
	if n := store.calls.Load(); n != 0 {
		t.Errorf("blacklist store called %d times for tokens that did not verify", n)
	}
	if gen, _ := mgr.CurrentRefreshGeneration("user-1"); gen != 0 {
		t.Errorf("refresh generation = %d, want 0: a token that did not verify bumped it", gen)
	}
	if _, err := mgr.RefreshToken(refresh, &jwtRefreshTestStore{user: user}); err != nil {
		t.Errorf("RefreshToken after the refused sign-outs = %v, want a token", err)
	}
}

// flakyGenerationStore fails Bump while failing is set.
type flakyGenerationStore struct {
	*InMemoryRefreshGenerationStore
	failing atomic.Bool
}

func (s *flakyGenerationStore) Bump(userID string) (int64, error) {
	if s.failing.Load() {
		return 0, errHostileGeneration
	}
	return s.InMemoryRefreshGenerationStore.Bump(userID)
}

// A sign-out whose blacklist add landed and whose generation bump failed
// is completed by a retry with the same token: the add answers "already
// present" and the bump is attempted all the same, so the refresh token
// issued before stops minting.
func TestJWT_SignOutToken_FailedBump_RetryCompletes(t *testing.T) {
	store := NewInMemoryBlacklistStore()
	mgr := newConsumeManager(t, store, true)
	gens := &flakyGenerationStore{InMemoryRefreshGenerationStore: NewInMemoryRefreshGenerationStore()}
	mgr.SetRefreshGenerationStore(gens)
	user := &jwtRefreshTestUser{id: "user-1"}
	users := &jwtRefreshTestStore{user: user}
	token, err := mgr.GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	refresh, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	gens.failing.Store(true)
	claims, err := mgr.SignOutToken(token)
	if claims == nil || !errors.Is(err, errHostileGeneration) {
		t.Fatalf("SignOutToken with the generation store down = (%v, %v), want claims and its error", claims, err)
	}
	if !yes(store.IsBlacklisted(claims.ID)) {
		t.Fatal("test setup error: the blacklist add did not land")
	}
	gens.failing.Store(false)
	if gen, _ := mgr.CurrentRefreshGeneration("user-1"); gen != 0 {
		t.Fatalf("test setup error: generation = %d after the failed bump, want 0", gen)
	}

	if _, err := mgr.SignOutToken(token); err != nil {
		t.Fatalf("retry of the failed sign-out = %v, want nil", err)
	}
	if _, err := mgr.RefreshToken(refresh, users); !errors.Is(err, ErrRefreshGenerationStale) {
		t.Fatalf("RefreshToken after the retried sign-out = %v, want ErrRefreshGenerationStale: the retry did not complete the sign-out", err)
	}
}

// matchAllError is a store error whose Is answers true for every target.
type matchAllError struct{}

func (matchAllError) Error() string { return "store error that matches everything" }
func (matchAllError) Is(error) bool { return true }

// matchAllBlacklist fails its read with matchAllError.
type matchAllBlacklist struct{ *InMemoryBlacklistStore }

func (matchAllBlacklist) IsBlacklisted(string) (bool, error) { return false, matchAllError{} }

// A store error that claims to be every error is still an outage: the
// refresh reports ErrBlacklistUnavailable with its cause, not a bare
// "already used".
func TestJWT_RefreshToken_StoreErrorMatchingEverything_StaysAnOutage(t *testing.T) {
	user := &jwtRefreshTestUser{id: "user-1"}
	users := &jwtRefreshTestStore{user: user}
	mgr := newConsumeManager(t, matchAllBlacklist{NewInMemoryBlacklistStore()}, true)
	refresh, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	token, err := mgr.RefreshToken(refresh, users)
	if token != "" {
		t.Fatal("RefreshToken issued a token while the store could not answer")
	}
	if err == ErrRefreshTokenUsed {
		t.Fatal("RefreshToken returned the bare ErrRefreshTokenUsed for a store outage: the outage and its cause are lost")
	}
	var cause matchAllError
	if !errors.As(err, &cause) {
		t.Fatalf("RefreshToken error = %v, want it to keep the store's cause", err)
	}
	if got := err.Error(); !strings.HasPrefix(got, ErrBlacklistUnavailable.Error()) {
		t.Fatalf("RefreshToken error = %q, want the outage error", got)
	}
	if users.findByIDCalls != 0 {
		t.Errorf("user store asked %d times for a refresh refused at validation", users.findByIDCalls)
	}
}

// The manager keeps one reference to the blacklist store, the one blStore
// hands out: its config copy holds none, so no code path can reach the
// store around the contained calls.
func TestJWT_Manager_KeepsOneStoreReference(t *testing.T) {
	store := NewInMemoryBlacklistStore()
	mgr := newConsumeManager(t, store, true)
	if mgr.config.BlacklistStore != nil {
		t.Fatal("the manager's config copy still holds the blacklist store")
	}
	if mgr.blStore() != BlacklistStore(store) {
		t.Fatal("the manager does not use the configured store")
	}
	if !mgr.config.BlacklistEnabled {
		t.Fatal("the manager's config lost BlacklistEnabled")
	}
}

// A refresh token from before a generation bump holds nothing: presented
// for sign-out it is refused before either write.
func TestJWT_SignOutToken_StaleRefreshToken_WritesNothing(t *testing.T) {
	store := newHostileBlacklist()
	mgr := newConsumeManager(t, store, true)
	user := &jwtRefreshTestUser{id: "user-1"}
	stale, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	if _, err := mgr.BumpRefreshGeneration("user-1"); err != nil {
		t.Fatalf("BumpRefreshGeneration: %v", err)
	}
	current, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	calls := store.calls.Load()

	claims, err := mgr.SignOutToken(stale)
	if claims != nil || !errors.Is(err, ErrRefreshGenerationStale) {
		t.Fatalf("SignOutToken(stale refresh) = (%v, %v), want (nil, ErrRefreshGenerationStale)", claims, err)
	}
	if n := store.calls.Load(); n != calls {
		t.Errorf("blacklist store called %d times for a stale refresh token", n-calls)
	}
	if gen, _ := mgr.CurrentRefreshGeneration("user-1"); gen != 1 {
		t.Errorf("refresh generation = %d, want 1: a stale refresh token bumped it", gen)
	}
	if _, err := mgr.RefreshToken(current, &jwtRefreshTestStore{user: user}); err != nil {
		t.Errorf("RefreshToken of the user's current refresh token = %v: the stale token ended it", err)
	}
}

// downGenerationStore fails every Current and counts its Bumps.
type downGenerationStore struct {
	*InMemoryRefreshGenerationStore
	bumps atomic.Int32
}

func (s *downGenerationStore) Current(string) (int64, error) { return 0, errHostileGeneration }
func (s *downGenerationStore) Bump(userID string) (int64, error) {
	s.bumps.Add(1)
	return s.InMemoryRefreshGenerationStore.Bump(userID)
}

// A generation store that cannot answer the check: the refresh token's
// sign-out is refused with an error and nothing is written. An access
// token needs no check and is signed out as before.
func TestJWT_SignOutToken_GenerationCheckFails_RefreshRefusedNothingWritten(t *testing.T) {
	store := newHostileBlacklist()
	mgr := newConsumeManager(t, store, true)
	user := &jwtRefreshTestUser{id: "user-1"}
	refresh, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	access, err := mgr.GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	gens := &downGenerationStore{InMemoryRefreshGenerationStore: NewInMemoryRefreshGenerationStore()}
	mgr.SetRefreshGenerationStore(gens)
	calls := store.calls.Load()

	claims, err := mgr.SignOutToken(refresh)
	if err == nil || errors.Is(err, ErrRefreshGenerationStale) {
		t.Fatalf("SignOutToken(refresh) with the generation store down = %v, want the store's failure", err)
	}
	if claims == nil {
		t.Fatal("SignOutToken returned no claims: a store failure must not look like a token that did not verify")
	}
	if n := store.calls.Load(); n != calls {
		t.Errorf("blacklist store called %d times though the generation check failed", n-calls)
	}
	if n := gens.bumps.Load(); n != 0 {
		t.Errorf("generation bumped %d times though the generation check failed", n)
	}

	if _, err := mgr.SignOutToken(access); err != nil {
		t.Fatalf("SignOutToken(access) with the generation read down = %v, want nil: access tokens take no generation check", err)
	}
	if n := gens.bumps.Load(); n != 1 {
		t.Errorf("generation bumped %d times for the access token, want 1", n)
	}
}

// The retry rule holds for a refresh token too: its add lands, the bump
// fails, and the same token (still of the current generation) completes
// the sign-out.
func TestJWT_SignOutToken_RefreshToken_FailedBump_RetryCompletes(t *testing.T) {
	mgr := newConsumeManager(t, NewInMemoryBlacklistStore(), true)
	gens := &flakyGenerationStore{InMemoryRefreshGenerationStore: NewInMemoryRefreshGenerationStore()}
	mgr.SetRefreshGenerationStore(gens)
	user := &jwtRefreshTestUser{id: "user-1"}
	refresh, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	other, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	gens.failing.Store(true)
	if _, err := mgr.SignOutToken(refresh); !errors.Is(err, errHostileGeneration) {
		t.Fatalf("SignOutToken with the bump failing = %v, want the generation store's error", err)
	}
	gens.failing.Store(false)
	if _, err := mgr.SignOutToken(refresh); err != nil {
		t.Fatalf("retry of the failed sign-out = %v, want nil", err)
	}
	if _, err := mgr.RefreshToken(other, &jwtRefreshTestStore{user: user}); !errors.Is(err, ErrRefreshGenerationStale) {
		t.Fatalf("RefreshToken of the user's other refresh token = %v, want ErrRefreshGenerationStale: the retry did not complete the sign-out", err)
	}
}
