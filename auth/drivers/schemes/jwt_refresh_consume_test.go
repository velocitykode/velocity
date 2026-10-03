package schemes

import (
	"errors"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// One refresh token presented to the scheme by 32 callers at once buys one
// access token; every other caller, and a later reuse, gets
// auth.ErrRefreshTokenUsed unwrapped by the scheme.
func TestJWTScheme_RefreshToken_Concurrent_OneSuccess(t *testing.T) {
	const callers = 32
	for round := range 10 {
		scheme := mustNewJWTScheme(&mockJWTUserStore{}, newTestJWTConfig())
		refresh, err := scheme.GenerateRefreshToken(&mockJWTUser{id: "user123"})
		if err != nil {
			t.Fatalf("round %d: GenerateRefreshToken: %v", round, err)
		}

		errs := make([]error, callers)
		tokens := make([]string, callers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range callers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				tokens[i], errs[i] = scheme.RefreshToken(refresh)
			}()
		}
		close(start)
		wg.Wait()

		won := 0
		for i, err := range errs {
			switch {
			case err == nil:
				won++
				if _, vErr := scheme.ValidateToken(tokens[i]); vErr != nil {
					t.Errorf("round %d caller %d: issued token does not validate: %v", round, i, vErr)
				}
			case !errors.Is(err, auth.ErrRefreshTokenUsed):
				t.Errorf("round %d caller %d: error = %v, want nil or auth.ErrRefreshTokenUsed", round, i, err)
			}
		}
		if won != 1 {
			t.Fatalf("round %d: %d of %d concurrent refreshes succeeded, want exactly 1", round, won, callers)
		}
		if _, err := scheme.RefreshToken(refresh); !errors.Is(err, auth.ErrRefreshTokenUsed) {
			t.Fatalf("round %d: reuse after the consume = %v, want auth.ErrRefreshTokenUsed", round, err)
		}
		scheme.StopCleanup()
	}
}
