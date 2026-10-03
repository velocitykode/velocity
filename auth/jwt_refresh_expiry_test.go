package auth

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/velocitykode/velocity/contract"
)

// latchedUserStore parks every FindByID until release is closed and
// reports each arrival on entered: the caller is past ValidateToken and
// the generation check, and has not reached the consume.
type latchedUserStore struct {
	consumeUserStore
	entered chan struct{}
	release chan struct{}
}

func (s *latchedUserStore) FindByID(id interface{}) (contract.Authenticatable, error) {
	s.entered <- struct{}{}
	<-s.release
	return s.consumeUserStore.FindByID(id)
}

func newLatchedUserStore(callers int) *latchedUserStore {
	return &latchedUserStore{
		consumeUserStore: consumeUserStore{user: &jwtRefreshTestUser{id: "u-1"}},
		entered:          make(chan struct{}, callers),
		release:          make(chan struct{}),
	}
}

// shortRefreshToken signs a refresh token for the manager that expires on
// a whole second between one and two seconds from now (the exp claim has
// second precision).
func shortRefreshToken(t *testing.T, mgr *JWTManager, jti string) (string, time.Time) {
	t.Helper()
	now := time.Now()
	exp := now.Truncate(time.Second).Add(2 * time.Second)
	return signClaims(t, mgr, Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   "u-1",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
		UserID:    "u-1",
		TokenType: "refresh",
	}), exp
}

// awaitEntered waits for n callers to be parked in the user lookup. A
// caller that returned instead (its validation ran after the expiry on a
// starved machine) fails the test rather than hanging it.
func awaitEntered(t *testing.T, users *latchedUserStore, n int, done <-chan struct{}) {
	t.Helper()
	for i := range n {
		select {
		case <-users.entered:
		case <-done:
			t.Fatalf("the callers returned with %d of %d parked in the user lookup: validation did not finish before the expiry", i, n)
		case <-time.After(30 * time.Second):
			t.Fatalf("%d of %d callers reached the user lookup", i, n)
		}
	}
}

// awaitExpired returns once the manager itself refuses the token as
// expired: the evidence the latch is released on.
func awaitExpired(t *testing.T, mgr *JWTManager, refresh string, exp time.Time) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := mgr.ValidateToken(refresh)
		if errors.Is(err, jwt.ErrTokenExpired) {
			return
		}
		// Before the expiry a consumed token is refused as revoked: the
		// parser checks the expiry first, so that is "not expired yet".
		if err != nil && !errors.Is(err, errTokenRevoked) {
			t.Fatalf("ValidateToken while waiting for the expiry: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("token with exp %v still validates", exp)
		}
		time.Sleep(time.Until(exp) + time.Millisecond)
	}
}

// A refresh token that every caller validated before its expiry, and that
// expired while they were all in the user lookup, buys nothing. At the
// base the first Add wrote an entry that was already expired, every later
// Add read it as absent, and each of the 32 callers was issued a token.
func TestJWT_RefreshToken_ExpiresBeforeConsume_NoneIssued(t *testing.T) {
	t.Parallel()
	store := NewInMemoryBlacklistStore()
	mgr := newConsumeManager(t, store, true)
	refresh, exp := shortRefreshToken(t, mgr, "jti-expires-in-lookup")
	users := newLatchedUserStore(refreshConsumeCallers)

	tokens := make([]string, refreshConsumeCallers)
	errs := make([]error, refreshConsumeCallers)
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := range refreshConsumeCallers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], errs[i] = mgr.RefreshToken(refresh, users)
		}()
	}
	go func() { wg.Wait(); close(done) }()
	release := sync.OnceFunc(func() { close(users.release) })
	defer func() { <-done }()
	defer release()

	awaitEntered(t, users, refreshConsumeCallers, done)
	awaitExpired(t, mgr, refresh, exp)
	release()
	<-done

	won := 0
	for i, err := range errs {
		if err == nil {
			won++
			continue
		}
		if tokens[i] != "" {
			t.Errorf("caller %d: a token came back with error %v", i, err)
		}
		if !errors.Is(err, jwt.ErrTokenExpired) {
			t.Errorf("caller %d: error = %v, want jwt.ErrTokenExpired", i, err)
		}
		if errors.Is(err, ErrRefreshTokenUsed) {
			t.Errorf("caller %d: an expired token was reported as used", i)
		}
	}
	if won != 0 {
		t.Fatalf("%d of %d refreshes of a token that expired before the consume were issued a token, want 0", won, refreshConsumeCallers)
	}
	store.mu.RLock()
	_, written := store.entries["jti-expires-in-lookup"]
	store.mu.RUnlock()
	if written {
		t.Error("a consume past the token's expiry wrote a blacklist entry")
	}
}

// One caller consumes the token before its expiry; the others validated
// before the expiry too and reach the consume after it. The entry the
// winner wrote has lapsed by then: at the base each late Add read it as
// absent and issued. Exactly one token is issued, and no late caller wins.
func TestJWT_RefreshToken_WinnerBeforeExpiry_LateCallersRefused(t *testing.T) {
	t.Parallel()
	store := NewInMemoryBlacklistStore()
	mgr := newConsumeManager(t, store, true)
	refresh, exp := shortRefreshToken(t, mgr, "jti-winner-then-expiry")
	const late = refreshConsumeCallers - 1
	users := newLatchedUserStore(late)

	tokens := make([]string, late)
	errs := make([]error, late)
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := range late {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], errs[i] = mgr.RefreshToken(refresh, users)
		}()
	}
	go func() { wg.Wait(); close(done) }()
	release := sync.OnceFunc(func() { close(users.release) })
	defer func() { <-done }()
	defer release()

	awaitEntered(t, users, late, done)

	// The winner: not latched, consumes while the token is still valid.
	access, err := mgr.RefreshToken(refresh, &consumeUserStore{user: &jwtRefreshTestUser{id: "u-1"}})
	if err != nil {
		t.Fatalf("refresh before the expiry: %v (exp %v, now %v)", err, exp, time.Now())
	}
	if _, err := mgr.ValidateAccessToken(access); err != nil {
		t.Fatalf("issued token does not validate: %v", err)
	}

	awaitExpired(t, mgr, refresh, exp)
	release()
	<-done

	for i, err := range errs {
		if err == nil {
			t.Errorf("late caller %d was issued a token after the winner consumed it and the entry lapsed", i)
			continue
		}
		if tokens[i] != "" {
			t.Errorf("late caller %d: a token came back with error %v", i, err)
		}
		if !errors.Is(err, jwt.ErrTokenExpired) {
			t.Errorf("late caller %d: error = %v, want jwt.ErrTokenExpired", i, err)
		}
	}
}

// Add's rule for a deadline that has passed: false, nothing written,
// whatever the blacklist holds for the JTI. At the base the first such
// Add returned true and so did every repeat.
func TestInMemoryBlacklistStore_Add_RefusesPassedDeadline(t *testing.T) {
	store := NewInMemoryBlacklistStore()
	past := time.Now().Add(-time.Millisecond)
	future := time.Now().Add(time.Hour)

	entry := func(jti string) (time.Time, bool) {
		store.mu.RLock()
		defer store.mu.RUnlock()
		at, ok := store.entries[jti]
		return at, ok
	}

	// No entry: every caller is refused, concurrently and in sequence.
	var consumed int
	var mu sync.Mutex
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range refreshConsumeCallers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if store.Add("absent", past) {
				mu.Lock()
				consumed++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if consumed != 0 {
		t.Fatalf("%d of %d Adds with a passed deadline consumed, want 0", consumed, refreshConsumeCallers)
	}
	if _, ok := entry("absent"); ok {
		t.Fatal("an Add with a passed deadline wrote an entry")
	}
	if store.IsBlacklisted("absent") {
		t.Fatal("a JTI Added with a passed deadline reads as blacklisted")
	}
	if store.Add("zero", time.Time{}) {
		t.Fatal("Add with the zero time consumed")
	}

	// An expired entry: still refused, and the JTI stays consumable by a
	// call with a live deadline.
	seedExpiredEntry(store, "lapsed")
	if store.Add("lapsed", past) {
		t.Fatal("Add with a passed deadline over an expired entry consumed")
	}
	if !store.Add("lapsed", future) {
		t.Fatal("Add with a live deadline over an expired entry = false, want true")
	}

	// A live entry: refused, entry untouched.
	if store.Add("lapsed", past) {
		t.Fatal("Add with a passed deadline over a live entry consumed")
	}
	if at, ok := entry("lapsed"); !ok || !at.Equal(future) {
		t.Fatalf("live entry after an Add with a passed deadline = %v, %v; want %v kept", at, ok, future)
	}
}

// RevokeToken with an expiry that has passed writes nothing: the token no
// longer validates, and a dead entry would only wait for Cleanup.
func TestJWT_RevokeToken_PassedExpiry_WritesNothing(t *testing.T) {
	store := NewInMemoryBlacklistStore()
	mgr := newConsumeManager(t, store, true)
	mgr.RevokeToken("gone", time.Now().Add(-time.Second))
	store.mu.RLock()
	n := len(store.entries)
	store.mu.RUnlock()
	if n != 0 {
		t.Fatalf("blacklist holds %d entries after revoking an expired token, want 0", n)
	}
	if mgr.IsBlacklisted("gone") {
		t.Fatal("an expired revocation reads as blacklisted")
	}
}
