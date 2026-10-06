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
	"github.com/velocitykode/velocity/internal/panicerr"
)

const refreshConsumeCallers = 32

// consumeUserStore resolves one user for any id, or fails with err. A nil
// user comes back as a typed nil unless untypedNil is set. It is safe for
// concurrent callers once its fields are set.
type consumeUserStore struct {
	user       *jwtRefreshTestUser
	untypedNil bool
	err        atomic.Pointer[error]
}

func (p *consumeUserStore) FindByID(interface{}) (contract.Authenticatable, error) {
	if e := p.err.Load(); e != nil {
		return nil, *e
	}
	if p.user == nil && p.untypedNil {
		return nil, nil
	}
	return p.user, nil
}
func (p *consumeUserStore) FindByIDCtx(_ context.Context, id interface{}) (contract.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *consumeUserStore) FindByCredentials(map[string]interface{}) (contract.Authenticatable, error) {
	return p.user, nil
}
func (p *consumeUserStore) FindByCredentialsCtx(context.Context, map[string]interface{}) (contract.Authenticatable, error) {
	return p.user, nil
}
func (p *consumeUserStore) ValidateCredentials(contract.Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *consumeUserStore) UpdateRememberToken(contract.Authenticatable, string) error { return nil }
func (p *consumeUserStore) UpdateRememberTokenCtx(context.Context, contract.Authenticatable, string) error {
	return nil
}

func newConsumeManager(t *testing.T, store BlacklistStore, enabled bool) *JWTManager {
	t.Helper()
	mgr, err := NewJWTManager(JWTConfig{
		Secret:           strings.Repeat("s", 64),
		Algorithm:        "HS256",
		TTL:              60,
		RefreshTTL:       20160,
		BlacklistEnabled: enabled,
		BlacklistStore:   store,
	})
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}
	return mgr
}

// refreshConcurrently presents one refresh token from n callers released
// together and returns each caller's token and error.
func refreshConcurrently(mgr *JWTManager, refresh string, users UserStore, n int) ([]string, []error) {
	tokens := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tokens[i], errs[i] = mgr.RefreshToken(refresh, users)
		}()
	}
	close(start)
	wg.Wait()
	return tokens, errs
}

// assertOneRefresh asserts exactly one caller got an access token and
// every other caller got ErrRefreshTokenUsed and no token.
func assertOneRefresh(t *testing.T, mgr *JWTManager, tokens []string, errs []error) {
	t.Helper()
	won := 0
	for i, err := range errs {
		switch {
		case err == nil:
			won++
			if _, vErr := mgr.ValidateAccessToken(tokens[i]); vErr != nil {
				t.Errorf("caller %d: issued token does not validate: %v", i, vErr)
			}
		case errors.Is(err, ErrRefreshTokenUsed):
			if tokens[i] != "" {
				t.Errorf("caller %d: a token came back with ErrRefreshTokenUsed", i)
			}
		default:
			t.Errorf("caller %d: error = %v, want nil or ErrRefreshTokenUsed", i, err)
		}
	}
	if won != 1 {
		t.Fatalf("%d of %d concurrent refreshes with one token succeeded, want exactly 1", won, len(errs))
	}
}

// One refresh token presented by 32 callers at once buys one access token.
// At the base every caller that validated before the first blacklist write
// landed was issued a token.
func TestJWT_RefreshToken_Concurrent_OneSuccess(t *testing.T) {
	user := &jwtRefreshTestUser{id: "user-1"}
	users := &consumeUserStore{user: user}
	for round := range 20 {
		mgr := newConsumeManager(t, NewInMemoryBlacklistStore(), true)
		refresh, err := mgr.GenerateRefreshToken(user)
		if err != nil {
			t.Fatalf("round %d: GenerateRefreshToken: %v", round, err)
		}
		tokens, errs := refreshConcurrently(mgr, refresh, users, refreshConsumeCallers)
		assertOneRefresh(t, mgr, tokens, errs)

		// A later reuse gets the same sentinel as the racers that lost.
		if tok, err := mgr.RefreshToken(refresh, users); !errors.Is(err, ErrRefreshTokenUsed) || tok != "" {
			t.Fatalf("round %d: reuse after the consume = (%q, %v), want ErrRefreshTokenUsed", round, tok, err)
		}
	}
}

// gatedBlacklistStore holds every Add at a gate until released, so all
// callers are past validation and inside the consume at once: the widest
// form of the race. It delegates to the in-memory store.
type gatedBlacklistStore struct {
	*InMemoryBlacklistStore
	entered chan struct{}
	release chan struct{}
}

func (s *gatedBlacklistStore) Add(jti string, expiresAt time.Time) (bool, error) {
	s.entered <- struct{}{}
	<-s.release
	return s.InMemoryBlacklistStore.Add(jti, expiresAt)
}

// A store whose Add blocks: nothing is issued while the consume is
// pending, the manager holds no lock across the store call (a store swap
// goes through meanwhile), and once the store answers exactly one caller
// wins.
func TestJWT_RefreshToken_BlockingStore_OneSuccess(t *testing.T) {
	user := &jwtRefreshTestUser{id: "user-1"}
	users := &consumeUserStore{user: user}
	store := &gatedBlacklistStore{
		InMemoryBlacklistStore: NewInMemoryBlacklistStore(),
		entered:                make(chan struct{}, refreshConsumeCallers),
		release:                make(chan struct{}),
	}
	mgr := newConsumeManager(t, store, true)
	refresh, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	type result struct {
		tokens []string
		errs   []error
	}
	done := make(chan result, 1)
	go func() {
		tokens, errs := refreshConcurrently(mgr, refresh, users, refreshConsumeCallers)
		done <- result{tokens, errs}
	}()
	for range refreshConsumeCallers {
		<-store.entered
	}
	select {
	case r := <-done:
		t.Fatalf("refreshes returned while the store's Add was still blocked: %v", r.errs)
	default:
	}

	// Every caller is parked inside the store's Add. A swap takes the
	// manager's write lock: it returns only if no caller holds the read
	// lock across the store call. Swap the same store back in so the
	// parked callers and any later reader agree.
	swapped := make(chan struct{})
	go func() {
		mgr.SetBlacklistStore(store)
		close(swapped)
	}()
	<-swapped

	close(store.release)
	r := <-done
	assertOneRefresh(t, mgr, r.tokens, r.errs)
}

// panickingBlacklistStore panics in Add.
type panickingBlacklistStore struct {
	*InMemoryBlacklistStore
	value any
	adds  atomic.Int32
}

func (s *panickingBlacklistStore) Add(string, time.Time) (bool, error) {
	s.adds.Add(1)
	panic(s.value)
}

// A store whose Add panics: the panic is contained, every caller is
// refused with an error that carries it, and no access token is issued.
func TestJWT_RefreshToken_PanickingStore_IssuesNothing(t *testing.T) {
	user := &jwtRefreshTestUser{id: "user-1"}
	users := &consumeUserStore{user: user}
	boom := errors.New("blacklist backend down")
	store := &panickingBlacklistStore{InMemoryBlacklistStore: NewInMemoryBlacklistStore(), value: boom}
	mgr := newConsumeManager(t, store, true)
	refresh, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	tokens, errs := refreshConcurrently(mgr, refresh, users, refreshConsumeCallers)
	for i, err := range errs {
		if tokens[i] != "" {
			t.Errorf("caller %d: a token was issued although the consume panicked", i)
		}
		if err == nil {
			t.Fatalf("caller %d: nil error although the consume panicked", i)
		}
		if errors.Is(err, ErrRefreshTokenUsed) {
			t.Errorf("caller %d: a store failure reported as ErrRefreshTokenUsed", i)
		}
		if !errors.Is(err, boom) || panicerr.AsTyped(err) == nil {
			t.Errorf("caller %d: error %v does not carry the recovered panic", i, err)
		}
	}
	if got := store.adds.Load(); got != refreshConsumeCallers {
		t.Errorf("store Add ran %d times, want %d", got, refreshConsumeCallers)
	}
}

// A failure before the consume leaves the refresh token usable: the user
// store fails, the retry succeeds, and only then is the token spent.
func TestJWT_RefreshToken_UserStoreFailure_TokenStillUsable(t *testing.T) {
	user := &jwtRefreshTestUser{id: "user-1"}
	users := &consumeUserStore{user: user}
	store := NewInMemoryBlacklistStore()
	mgr := newConsumeManager(t, store, true)
	refresh, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	claims, err := mgr.ValidateToken(refresh)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}

	down := errors.New("user store down")
	users.err.Store(&down)
	if _, err := mgr.RefreshToken(refresh, users); !errors.Is(err, down) {
		t.Fatalf("RefreshToken with a failing user store = %v, want the store's error", err)
	}
	if yes(store.IsBlacklisted(claims.ID)) {
		t.Fatal("refresh token consumed although the user lookup failed")
	}

	// A user deleted since issuance is the same, whether the store answers
	// with a nil interface or a typed nil: nothing is consumed.
	users.err.Store(nil)
	users.user = nil
	for _, untyped := range []bool{true, false} {
		users.untypedNil = untyped
		if _, err := mgr.RefreshToken(refresh, users); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("RefreshToken for a missing user (untyped nil %v) = %v, want ErrUserNotFound", untyped, err)
		}
		if yes(store.IsBlacklisted(claims.ID)) {
			t.Fatalf("refresh token consumed although the user was not found (untyped nil %v)", untyped)
		}
	}

	users.user = user
	if tok, err := mgr.RefreshToken(refresh, users); err != nil || tok == "" {
		t.Fatalf("retry after the user store recovered = (%q, %v), want a token", tok, err)
	}
	if _, err := mgr.RefreshToken(refresh, users); !errors.Is(err, ErrRefreshTokenUsed) {
		t.Fatalf("reuse after the successful retry = %v, want ErrRefreshTokenUsed", err)
	}
}

// A stale generation is refused before the consume: the token is not
// spent on the blacklist by a refusal.
func TestJWT_RefreshToken_StaleGeneration_NotConsumed(t *testing.T) {
	user := &jwtRefreshTestUser{id: "user-1"}
	users := &consumeUserStore{user: user}
	store := NewInMemoryBlacklistStore()
	mgr := newConsumeManager(t, store, true)
	refresh, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	claims, err := mgr.ValidateToken(refresh)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if _, err := mgr.BumpRefreshGeneration(user.id); err != nil {
		t.Fatalf("BumpRefreshGeneration: %v", err)
	}
	if _, err := mgr.RefreshToken(refresh, users); !errors.Is(err, ErrRefreshGenerationStale) {
		t.Fatalf("RefreshToken after the bump = %v, want ErrRefreshGenerationStale", err)
	}
	if yes(store.IsBlacklisted(claims.ID)) {
		t.Fatal("a stale refresh token was written to the blacklist")
	}
}

// A failure after the consume burns the token: signing fails, nothing is
// issued, and the token cannot be presented again.
func TestJWT_RefreshToken_GenerateFailure_BurnsToken(t *testing.T) {
	user := &jwtRefreshTestUser{id: "user-1"}
	users := &consumeUserStore{user: user}
	mgr := newConsumeManager(t, NewInMemoryBlacklistStore(), true)
	refresh, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	orig := randReader
	randReader = failingReader{}
	_, err = mgr.RefreshToken(refresh, users)
	randReader = orig
	if err == nil || errors.Is(err, ErrRefreshTokenUsed) {
		t.Fatalf("RefreshToken with a failing JTI source = %v, want the generate error", err)
	}
	if _, err := mgr.RefreshToken(refresh, users); !errors.Is(err, ErrRefreshTokenUsed) {
		t.Fatalf("retry after a failed issuance = %v, want ErrRefreshTokenUsed (fail closed)", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("entropy source failed") }

// With the blacklist off there is no one-shot refresh: every caller of 32
// gets a token, the store is never written, and the token stays usable.
func TestJWT_RefreshToken_BlacklistDisabled_Reusable(t *testing.T) {
	user := &jwtRefreshTestUser{id: "user-1"}
	users := &consumeUserStore{user: user}
	store := &panickingBlacklistStore{InMemoryBlacklistStore: NewInMemoryBlacklistStore(), value: "must not be called"}
	mgr := newConsumeManager(t, store, false)
	refresh, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	tokens, errs := refreshConcurrently(mgr, refresh, users, refreshConsumeCallers)
	for i, err := range errs {
		if err != nil || tokens[i] == "" {
			t.Fatalf("caller %d: (%q, %v), want a token: the blacklist is off", i, tokens[i], err)
		}
	}
	if got := store.adds.Load(); got != 0 {
		t.Fatalf("store Add ran %d times with the blacklist off, want 0", got)
	}
	if _, err := mgr.RefreshToken(refresh, users); err != nil {
		t.Fatalf("reuse with the blacklist off = %v, want nil", err)
	}
}

// A refresh JTI the app revoked itself reports as used on refresh, and a
// blacklisted access token keeps ValidateToken's revoked answer.
func TestJWT_RefreshToken_RevokedJTI(t *testing.T) {
	user := &jwtRefreshTestUser{id: "user-1"}
	users := &consumeUserStore{user: user}
	mgr := newConsumeManager(t, NewInMemoryBlacklistStore(), true)
	refresh, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	claims, err := mgr.ValidateToken(refresh)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	noErr(mgr.RevokeToken(claims.ID, claims.ExpiresAt.Time))
	noErr(mgr.RevokeToken(claims.ID, claims.ExpiresAt.Time)) // idempotent
	if _, err := mgr.RefreshToken(refresh, users); !errors.Is(err, ErrRefreshTokenUsed) {
		t.Fatalf("RefreshToken of a revoked JTI = %v, want ErrRefreshTokenUsed", err)
	}
	_, err = mgr.ValidateToken(refresh)
	if err == nil || err.Error() != "velocity/auth: token has been revoked" || errors.Is(err, ErrRefreshTokenUsed) {
		t.Fatalf("ValidateToken of a revoked JTI = %v, want the revoked error", err)
	}
}

// Add's contract on the in-memory store: of 32 concurrent calls with one
// JTI exactly one consumes; a losing call leaves the entry's expiry alone;
// an expired entry counts as absent.
func TestInMemoryBlacklistStore_Add_ReportsConsumed(t *testing.T) {
	store := NewInMemoryBlacklistStore()
	future := time.Now().Add(time.Hour)

	for round := range 50 {
		jti := "jti-" + string(rune('a'+round%26)) + "-" + time.Duration(round).String()
		var consumed atomic.Int32
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range refreshConsumeCallers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if yes(store.Add(jti, future)) {
					consumed.Add(1)
				}
				_ = yes(store.IsBlacklisted(jti))
			}()
		}
		close(start)
		wg.Wait()
		if got := consumed.Load(); got != 1 {
			t.Fatalf("round %d: %d concurrent Adds consumed one JTI, want exactly 1", round, got)
		}
	}

	// A losing Add never shortens the entry.
	if !yes(store.Add("keep", future)) {
		t.Fatal("first Add = false, want true")
	}
	if yes(store.Add("keep", time.Now().Add(-time.Hour))) {
		t.Fatal("second Add = true, want false")
	}
	if !yes(store.IsBlacklisted("keep")) {
		t.Fatal("a losing Add with a past expiry removed the live entry")
	}

	// A losing Add with a later expiry extends it: a JTI revoked for a
	// short while and then for longer stays revoked for the longer time.
	soon := time.Now().Add(time.Minute)
	if !yes(store.Add("extend", soon)) {
		t.Fatal("first Add = false, want true")
	}
	if yes(store.Add("extend", future)) {
		t.Fatal("second Add = true, want false")
	}
	store.mu.RLock()
	got := store.entries["extend"]
	store.mu.RUnlock()
	if !got.Equal(future) {
		t.Fatalf("entry expiry after a longer revocation = %v, want %v", got, future)
	}
	if yes(store.Add("extend", soon)) {
		t.Fatal("third Add = true, want false")
	}
	store.mu.RLock()
	got = store.entries["extend"]
	store.mu.RUnlock()
	if !got.Equal(future) {
		t.Fatalf("a shorter revocation moved the expiry to %v, want %v kept", got, future)
	}

	// An expired entry is absent: Add consumes again, with or without an
	// IsBlacklisted or Cleanup in between.
	seedExpiredEntry(store, "old")
	if yes(store.IsBlacklisted("old")) {
		t.Fatal("an expired entry reads as blacklisted")
	}
	if !yes(store.Add("old", future)) {
		t.Fatal("Add over an expired entry = false, want true")
	}
	if !yes(store.IsBlacklisted("old")) {
		t.Fatal("entry re-added over an expired one is not blacklisted")
	}
	seedExpiredEntry(store, "old2")
	if !yes(store.Add("old2", future)) {
		t.Fatal("Add over an expired entry (no read between) did not consume")
	}
	noErr(store.Cleanup())
	if !yes(store.IsBlacklisted("old2")) || !yes(store.IsBlacklisted("keep")) {
		t.Fatal("Cleanup removed a live entry")
	}
}

// seedExpiredEntry leaves an entry whose expiry has passed, as a live
// entry becomes once its time is up. Add refuses a passed deadline, so the
// entry is written directly.
func seedExpiredEntry(store *InMemoryBlacklistStore, jti string) {
	store.mu.Lock()
	store.entries[jti] = time.Now().Add(-time.Second)
	store.mu.Unlock()
}

// An expired entry read by IsBlacklisted is dropped only if it is still
// the expired one: a live entry Added between the read and the delete
// stays. Readers of an expired JTI race consumers of it; once a consume
// landed the JTI stays blacklisted.
func TestInMemoryBlacklistStore_IsBlacklisted_KeepsConcurrentAdd(t *testing.T) {
	future := time.Now().Add(time.Hour)
	for round := range 200 {
		store := NewInMemoryBlacklistStore()
		seedExpiredEntry(store, "jti")
		start := make(chan struct{})
		var wg sync.WaitGroup
		var consumed atomic.Int32
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if i%2 == 0 {
					_ = yes(store.IsBlacklisted("jti"))
					return
				}
				if yes(store.Add("jti", future)) {
					consumed.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if got := consumed.Load(); got != 1 {
			t.Fatalf("round %d: %d Adds consumed the expired JTI, want exactly 1", round, got)
		}
		if !yes(store.IsBlacklisted("jti")) {
			t.Fatalf("round %d: the live entry was deleted by a reader of the expired one", round)
		}
	}
}

// signClaims signs claims with the manager's own key: a correctly signed
// token this manager did not mint.
func signClaims(t *testing.T, mgr *JWTManager, claims Claims) string {
	t.Helper()
	method, err := mgr.getSigningMethod()
	if err != nil {
		t.Fatalf("getSigningMethod: %v", err)
	}
	tok, err := jwt.NewWithClaims(method, claims).SignedString(mgr.signingKey())
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}
	return tok
}

// A correctly signed token without exp or without jti is refused with
// ErrTokenClaimMissing on every path, access and refresh, blacklist on or
// off. At the base the refresh token without exp reached a nil dereference
// at the blacklist step, and tokens without a jti shared one blacklist
// key, so one refresh spent them all.
func TestJWT_ValidateToken_RequiresExpiryAndID(t *testing.T) {
	user := &jwtRefreshTestUser{id: "user-1"}
	users := &consumeUserStore{user: user}
	future := jwt.NewNumericDate(time.Now().Add(time.Hour))
	for _, enabled := range []bool{true, false} {
		for _, typ := range []string{"access", "refresh"} {
			tests := []struct {
				name   string
				claims jwt.RegisteredClaims
			}{
				{name: "no exp", claims: jwt.RegisteredClaims{ID: "jti-1", Subject: user.id}},
				{name: "no jti", claims: jwt.RegisteredClaims{ExpiresAt: future, Subject: user.id}},
				{name: "neither", claims: jwt.RegisteredClaims{Subject: user.id}},
			}
			for _, tt := range tests {
				mgr := newConsumeManager(t, NewInMemoryBlacklistStore(), enabled)
				tok := signClaims(t, mgr, Claims{RegisteredClaims: tt.claims, UserID: user.id, TokenType: typ})
				if _, err := mgr.ValidateToken(tok); !errors.Is(err, ErrTokenClaimMissing) {
					t.Errorf("blacklist %v, %s, %s: ValidateToken = %v, want ErrTokenClaimMissing", enabled, typ, tt.name, err)
				}
				if _, err := mgr.ValidateAccessToken(tok); !errors.Is(err, ErrTokenClaimMissing) {
					t.Errorf("blacklist %v, %s, %s: ValidateAccessToken = %v, want ErrTokenClaimMissing", enabled, typ, tt.name, err)
				}
				if got, err := mgr.RefreshToken(tok, users); !errors.Is(err, ErrTokenClaimMissing) || got != "" {
					t.Errorf("blacklist %v, %s, %s: RefreshToken = (%q, %v), want ErrTokenClaimMissing", enabled, typ, tt.name, got, err)
				}
			}
		}
	}

	// Two refresh tokens without a jti: refusing one leaves no shared
	// blacklist entry behind.
	store := NewInMemoryBlacklistStore()
	mgr := newConsumeManager(t, store, true)
	tok := signClaims(t, mgr, Claims{RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: future, Subject: user.id}, UserID: user.id, TokenType: "refresh"})
	_, _ = mgr.RefreshToken(tok, users)
	if yes(store.IsBlacklisted("")) {
		t.Fatal("the empty JTI was written to the blacklist")
	}

	// A minted pair carries both and still validates.
	access, err := mgr.GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if _, err := mgr.ValidateAccessToken(access); err != nil {
		t.Fatalf("ValidateAccessToken of a minted token: %v", err)
	}
}
