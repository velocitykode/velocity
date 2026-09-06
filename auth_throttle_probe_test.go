package velocity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/crypto"
)

// probeUser / probeUsers are the review's fixtures: one user, a credential
// check that blocks until released so the test can observe what else is
// admitted while a trial is in flight.
type probeUser struct{}

func (*probeUser) GetAuthIdentifier() any   { return "u1" }
func (*probeUser) GetAuthPassword() string  { return "unused-test-hash" }
func (*probeUser) GetRememberToken() string { return "" }
func (*probeUser) SetRememberToken(string)  {}

type probeUsers struct {
	mu      sync.Mutex
	checks  int
	entered chan struct{}
	release chan struct{}
}

func (*probeUsers) FindByID(any) (auth.Authenticatable, error) { return &probeUser{}, nil }
func (*probeUsers) FindByIDCtx(context.Context, any) (auth.Authenticatable, error) {
	return &probeUser{}, nil
}
func (*probeUsers) FindByCredentials(map[string]any) (auth.Authenticatable, error) {
	return &probeUser{}, nil
}
func (*probeUsers) FindByCredentialsCtx(context.Context, map[string]any) (auth.Authenticatable, error) {
	return &probeUser{}, nil
}
func (u *probeUsers) ValidateCredentials(_ auth.Authenticatable, c map[string]any) bool {
	u.mu.Lock()
	u.checks++
	u.mu.Unlock()
	u.entered <- struct{}{}
	<-u.release
	return c["password"] == "test-only-correct-candidate"
}
func (*probeUsers) UpdateRememberToken(auth.Authenticatable, string) error { return nil }
func (*probeUsers) UpdateRememberTokenCtx(context.Context, auth.Authenticatable, string) error {
	return nil
}
func (u *probeUsers) verified() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.checks
}

// TestSessionScheme_CacheThrottler_ConcurrentProbe is the reviewer's
// probe against the real session scheme and the real cache-backed
// throttler: the identifier already holds `seeded` of 20 failures and 64
// concurrent candidates (one correct) arrive from 64 source addresses.
// With reserve-before-verify plus the admission slot, at most the
// remaining within-cap capacity plus one slot-admitted trial may reach
// verification; everything else is denied while those are in flight.
func TestSessionScheme_CacheThrottler_ConcurrentProbe(t *testing.T) {
	cases := []struct {
		name   string
		seeded int
		budget int // remaining within-cap attempts + 1 slot trial
	}{
		{name: "identifier at cap (20/20)", seeded: 20, budget: 1},
		{name: "identifier one below cap (19/20)", seeded: 19, budget: 2},
		{name: "identifier five below cap (15/20)", seeded: 15, budget: 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc, err := crypto.NewEncryptor(crypto.Config{Key: strings.Repeat("k", 32), Cipher: "AES-256-GCM"})
			if err != nil {
				t.Fatalf("NewEncryptor: %v", err)
			}
			users := &probeUsers{entered: make(chan struct{}, 128), release: make(chan struct{})}
			scheme, err := schemes.NewSessionScheme(users, auth.SessionConfig{
				Name: "probe_session", Lifetime: 120, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
			}, enc)
			if err != nil {
				t.Fatalf("NewSessionScheme: %v", err)
			}
			scheme.SetAttemptFloor(-1)
			// Warm the memoised dummy bcrypt hash: the first attempt at a
			// cost generates it, and 64 concurrent first attempts under
			// the race detector would otherwise outlast the slot hold.
			_ = auth.GetDummyBcryptHash(0)
			throttler := newTestDimensionedLoginThrottler(t, 5, 20, 50).withDelay(time.Second, time.Second)
			scheme.SetLoginThrottler(throttler)

			creds := func(password string) map[string]any {
				return map[string]any{"email": "victim@example.test", "password": password}
			}
			for i := 0; i < tc.seeded; i++ {
				r := httptest.NewRequest(http.MethodPost, "/login", nil)
				r.RemoteAddr = fmt.Sprintf("203.0.113.%d:4000", i+1)
				for _, key := range auth.ThrottleKeys(r, creds("x"), nil) {
					throttler.RecordFailure(r, key)
				}
			}

			const parallel = 64
			oks := make([]bool, parallel)
			errs := make([]error, parallel)
			finished := make(chan struct{}, parallel)
			var wg sync.WaitGroup
			for i := 0; i < parallel; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					password := "wrong"
					if i == parallel-1 {
						password = "test-only-correct-candidate"
					}
					r := httptest.NewRequest(http.MethodPost, "/login", nil)
					r.RemoteAddr = fmt.Sprintf("198.51.100.%d:5000", i+1)
					r = schemes.WithSessionContext(r)
					oks[i], errs[i] = scheme.Attempt(httptest.NewRecorder(), r, creds(password))
					finished <- struct{}{}
				}(i)
			}

			inFlight, denied := 0, 0
			timeout := time.After(5 * time.Second)
			for denied < parallel-tc.budget {
				select {
				case <-users.entered:
					inFlight++
					if inFlight > tc.budget {
						t.Fatalf("%d trials reached the credential check concurrently, want at most %d", inFlight, tc.budget)
					}
				case <-finished:
					denied++
				case <-timeout:
					t.Fatalf("only %d of %d attempts denied while the budget was in flight (in flight %d)", denied, parallel-tc.budget, inFlight)
				}
			}
			close(users.release)
			wg.Wait()

			if got := users.verified(); got > tc.budget {
				t.Fatalf("verified %d candidates, want at most %d", got, tc.budget)
			}
			succeeded := 0
			for i := range oks {
				switch {
				case oks[i]:
					succeeded++
				case errs[i] == nil, errors.Is(errs[i], auth.ErrLoginThrottled):
				default:
					t.Fatalf("attempt %d: unexpected error %v", i, errs[i])
				}
			}
			if succeeded > 1 {
				t.Fatalf("%d successes, want at most 1", succeeded)
			}
		})
	}
}
