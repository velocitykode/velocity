package schemes

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
// auth.ThrottleKeys). When the throttler implements
// contract.LoginReserver every key is reserved (counted) atomically
// before the credential check, so concurrent attempts cannot all see
// the same remaining capacity; a reserved attempt records no further
// failure. Otherwise the legacy Allow / RecordFailure sequence runs,
// which is best-effort under concurrency. The pair and IP dimensions
// deny before the credential check. The identifier dimension is shared across all
// source IPs, so a pre-check denial would let an attacker lock a
// victim out of their account from throwaway IPs; instead an
// over-cap identifier bucket runs the credential check and denies
// only when the credentials are wrong. The error is the same
// regardless of which dimension tripped so a caller cannot tell a
// per-IP lockout from a per-account one.
//
// Verify-first alone caps nothing once the pair and IP buckets are
// rotated (distributed guessing, or spoofed forwarded headers reaching
// the app): every rotated attempt reaches the credential check and a
// correct guess succeeds. So an over-cap identifier bucket also pays a
// bounded progressive delay (auth.IdentifierDelay, sourced from the
// throttler's contract.LoginDelayer or auth.DefaultIdentifierDelay)
// added to the Timebox floor. Right and wrong candidates pay the same
// delay, so the delay itself is not a correctness oracle; it bounds the
// account-level trial rate per request stream without locking the
// account holder out (they pay at most the ceiling once, and their
// RecordSuccess clears the bucket).
//
// A delay paid inside the request bounds only that connection's rate,
// so the over-cap attempt must also claim the identifier's single
// admission slot (auth.AdmitIdentifierTrial: the throttler's
// contract.LoginAdmitter when store-backed, else the scheme's
// per-process auth.LocalLoginAdmitter) for the length of the delay.
// While the slot is held every further attempt for that identifier,
// from any connection or source address, is denied before the
// credential check, so account-level trials are admitted one per delay
// window in aggregate. The denial is ErrLoginChallengeRequired when a
// LoginChallenge is configured (the account holder solves the app's
// challenge and is admitted without delay or slot) and ErrLoginThrottled
// otherwise. The challenge never bypasses the pair or IP caps.
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
	admitter *auth.LocalLoginAdmitter,
	challenge auth.LoginChallenge,
) (auth.Authenticatable, []string, bool, error) {
	keys := auth.ThrottleKeys(r, credentials, trustedProxies)
	// Consult every dimension even after one denies, so the denial path
	// does the same number of lookups regardless of which dimension
	// tripped (no per-dimension oracle).
	reserver, reserved := throttler.(contract.LoginReserver)
	hardDenied := false
	identifierDenied := false
	identifierKey := ""
	identifierDelay := time.Duration(0)
	for _, key := range keys {
		var (
			within bool
			delay  time.Duration
		)
		if reserved {
			within, delay = reserver.Reserve(r, key)
		} else {
			within = throttler.Allow(r, key)
		}
		if !within {
			if strings.HasPrefix(key, auth.ThrottleKeyIdentifierPrefix) {
				identifierDenied = true
				identifierKey = key
				identifierDelay = delay
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
		if reserved {
			return // the reservation already counted this attempt
		}
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

	// Over-cap identifier: claim the admission slot and extend the
	// floor by the progressive delay. The delay wraps the credential
	// check rather than following it so a wrong candidate cannot be
	// told from a right one by when the response arrives relative to
	// the sleep. A request that passes the configured challenge skips
	// both; it has already proven interactive.
	floor := attemptFloor
	if floor < 0 {
		floor = 0
	}
	if identifierDenied && !(challenge != nil && challenge(r)) {
		// The reservation path carries the delay derived from the same
		// atomic count as the over-cap decision; re-reading the counter
		// here could observe an expired window and pay nothing.
		delay := identifierDelay
		if !reserved {
			delay = auth.IdentifierDelay(throttler, r, identifierKey)
		}
		if delay < 0 {
			delay = 0
		}
		if !auth.AdmitIdentifierTrial(throttler, admitter, r, identifierKey, delay) {
			// Slot held by an in-flight or just-finished trial for
			// this identifier. Pad like a hard denial so the two are
			// not separable by timing, and do not record a failure:
			// the attempt never reached the credential check, and
			// counting it would let the attacker ratchet the delay
			// with requests that cost them nothing.
			auth.Timebox(attemptFloor, func() {})
			if challenge != nil {
				return nil, keys, false, auth.ErrLoginChallengeRequired
			}
			return nil, keys, false, auth.ErrLoginThrottled
		}
		floor += delay
	}
	auth.Timebox(floor, func() {
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
	// over cap (having paid the progressive delay above): that bucket
	// exists to slow distributed guessing, not to lock the account
	// holder out. The caller's RecordSuccess clears it.
	return user, keys, true, nil
}

// recordAttemptSuccess clears every throttle dimension for a successful
// login and releases the per-process admission slot so the account
// holder's next attempt is not queued behind the trial that just
// succeeded. Store-backed admitters release in their own RecordSuccess.
func recordAttemptSuccess(r *http.Request, keys []string, throttler contract.LoginThrottler, admitter *auth.LocalLoginAdmitter) {
	for _, key := range keys {
		throttler.RecordSuccess(r, key)
		if strings.HasPrefix(key, auth.ThrottleKeyIdentifierPrefix) {
			admitter.Release(key)
		}
	}
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
