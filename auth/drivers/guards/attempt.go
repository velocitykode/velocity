package guards

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
)

// attemptCredentials runs the credential-check phase shared by
// SessionScheme.Attempt and JWTScheme.Attempt: throttle-key derivation and
// Allow checks, the Timebox-wrapped user store lookup + password verify with
// the dummy-hash timing pad (H-09), and RecordFailure on every failure
// branch. The caller owns the success tail (session login + remember cookie
// vs. token generation), the PasswordNeedsRehashEvent emission, and the
// RecordSuccess calls (which must run only after the scheme-specific login
// succeeded), which is why keys are returned.
//
// One key per throttle dimension (pair / identifier / IP, see
// auth.ThrottleKeys). The pair and IP dimensions deny before the
// credential check. The identifier dimension is shared across all
// source IPs, so a pre-check denial would let an attacker lock a
// victim out of their account from throwaway IPs; instead an
// over-cap identifier bucket runs the credential check and denies
// only when the credentials are wrong. The error is the same
// regardless of which dimension tripped so a caller cannot tell a
// per-IP lockout from a per-account one.
//
// The credential-check phase runs inside auth.Timebox so the missing-user
// fast path and the wrong-password slow path pad to the same wall-clock
// duration (H-09 fix). When the user does not exist the configured hasher
// still runs against a dummy hash sized to the configured bcrypt cost
// (F2 fix) so the CPU profile matches the wrong-password branch; without
// this an attacker can probe valid emails by measuring response time even
// with a constant-time floor.
//
// Returns (user, keys, true, nil) when the credentials are valid. Returns
// ok == false with the error the scheme's Attempt must surface (nil,
// auth.ErrLoginThrottled, or auth.ErrInvalidCredentials) otherwise.
func attemptCredentials(
	r *http.Request,
	credentials map[string]interface{},
	userStore auth.UserStore,
	hasher auth.Hasher,
	throttler contract.LoginThrottler,
	attemptFloor time.Duration,
	trustedProxies []*net.IPNet,
) (auth.Authenticatable, []string, bool, error) {
	keys := auth.ThrottleKeys(r, credentials, trustedProxies)
	// Consult every dimension even after one denies, so the denial path
	// does the same number of Allow lookups regardless of which dimension
	// tripped (no per-dimension oracle).
	hardDenied := false
	identifierDenied := false
	for _, key := range keys {
		if !throttler.Allow(r, key) {
			if strings.HasPrefix(key, auth.ThrottleKeyIdentifierPrefix) {
				identifierDenied = true
			} else {
				hardDenied = true
			}
		}
	}
	if hardDenied {
		// Pad the pre-check denial to the attempt floor so it is not
		// separable by timing from an identifier-dimension denial,
		// which runs the full (timeboxed) credential check below.
		auth.Timebox(attemptFloor, func() {})
		return nil, keys, false, auth.ErrLoginThrottled
	}
	recordFailure := func() {
		for _, key := range keys {
			throttler.RecordFailure(r, key)
		}
	}

	var (
		user            auth.Authenticatable
		findErr         error
		credentialsOK   bool
		invalidCredErr  error
		password        string
		passwordTypedOK bool
	)

	// Size the dummy hash to the configured bcrypt cost (F2 fix): a
	// cost-10 dummy against a cost-14 real verify would leak ~400ms of
	// timing difference even with the AttemptFloor in place.
	dummyHash := dummyHashForHasher(hasher)
	auth.Timebox(attemptFloor, func() {
		user, findErr = userStore.FindByCredentialsCtx(r.Context(), credentials)
		password, passwordTypedOK = credentials["password"].(string)

		if findErr != nil || user == nil {
			// User does not exist: still run the hasher against
			// a dummy hash so the CPU profile matches the
			// wrong-password branch. The result is discarded.
			if passwordTypedOK {
				_ = hasher.Verify(password, string(dummyHash))
			} else {
				_ = hasher.Verify("", string(dummyHash))
			}
			return
		}
		if !passwordTypedOK {
			// Credential dict lacked a "password" string. Treat
			// as invalid; still run the dummy hash so timing
			// stays uniform across the branch.
			_ = hasher.Verify("", string(dummyHash))
			invalidCredErr = auth.ErrInvalidCredentials
			return
		}
		credentialsOK = userStore.ValidateCredentials(user, map[string]interface{}{"password": password})
	})

	if findErr != nil || user == nil {
		recordFailure()
		if identifierDenied {
			return nil, keys, false, auth.ErrLoginThrottled
		}
		return nil, keys, false, nil // User not found
	}
	if invalidCredErr != nil {
		recordFailure()
		if identifierDenied {
			return nil, keys, false, auth.ErrLoginThrottled
		}
		return nil, keys, false, invalidCredErr
	}
	if !credentialsOK {
		recordFailure()
		if identifierDenied {
			return nil, keys, false, auth.ErrLoginThrottled
		}
		return nil, keys, false, nil // Invalid password
	}

	// Valid credentials proceed even when the identifier dimension is
	// over cap: that bucket exists to cap distributed guessing, not to
	// lock the account holder out. The caller's RecordSuccess clears it.
	return user, keys, true, nil
}

// maybeEmitRehashEvent fires auth.PasswordNeedsRehashEvent through dispatch
// when hasher.NeedsRehash reports the stored hash is out of date (M-08).
// No-op when no dispatcher has been wired. A dispatcher error must not
// block the already-successful login: it is reported through warn when one
// is supplied and swallowed otherwise.
func maybeEmitRehashEvent(
	ctx context.Context,
	dispatch func(ctx context.Context, event any) error,
	hasher auth.Hasher,
	user auth.Authenticatable,
	schemeName string,
	warn func(msg string, kvs ...any),
) {
	if dispatch == nil || hasher == nil || user == nil {
		return
	}
	if !hasher.NeedsRehash(user.GetAuthPassword()) {
		return
	}
	if err := dispatch(ctx, auth.PasswordNeedsRehashEvent{
		UserID:     user.GetAuthIdentifier(),
		SchemeName: schemeName,
	}); err != nil && warn != nil {
		warn("velocity/auth: password needs-rehash event dispatch failed", "user_id", user.GetAuthIdentifier(), "error", err)
	}
}
