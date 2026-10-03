package velocity

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/nilval"
)

const (
	// Per-dimension defaults. The pair cap stays tight (5 wrong
	// passwords for one account from one IP). The identifier and IP
	// caps are deliberately looser: they aggregate unrelated traffic
	// (one account across all IPs, one IP across all accounts), so a
	// tight cap would penalise a victim's account or a shared NAT
	// egress cheaply. They exist to cap distributed brute force and
	// password spraying, not to replace the pair limit. The identifier
	// dimension is additionally verify-first in the schemes (see
	// auth.ThrottleKeys): an over-cap identifier bucket denies only
	// wrong-credential attempts, so it cannot lock the account holder
	// out.
	defaultLoginThrottleMaxAttempts           = 5
	defaultLoginThrottleIdentifierMaxAttempts = 20
	defaultLoginThrottleIPMaxAttempts         = 50
	defaultLoginThrottleDecay                 = 60 * time.Second
	loginThrottleCachePrefix                  = "velocity:auth:login:"
)

// Over-cap identifier attempts pay a bounded progressive delay (see
// contract.LoginDelayer): delayBase for the first attempt past the cap,
// doubling per further failure, never above delayMax. Both are env
// tunable (AUTH_LOGIN_IDENTIFIER_DELAY / _MAX); a base of 0 disables
// the delay entirely, which leaves the identifier dimension with no
// bound once the pair and IP buckets rotate.

type cacheLoginThrottler struct {
	// store is the cache store the counters live in: the default store
	// of the app's cache, moved by rebind when a module replaces
	// Services.Cache. Read once per call.
	store atomic.Pointer[throttleStore]
	// maxAttempts caps the (identifier, IP) pair dimension and is the
	// fallback for keys carrying no recognised dimension prefix.
	maxAttempts           int64
	identifierMaxAttempts int64
	ipMaxAttempts         int64
	decay                 time.Duration
	delayBase             time.Duration
	delayMax              time.Duration
}

// compile-time guarantee the cache throttler implements every contract.
var (
	_ contract.LoginThrottler = (*cacheLoginThrottler)(nil)
	_ contract.LoginDelayer   = (*cacheLoginThrottler)(nil)
	_ contract.LoginAdmitter  = (*cacheLoginThrottler)(nil)
	_ contract.LoginReserver  = (*cacheLoginThrottler)(nil)
)

// loginThrottleTrialSuffix marks the admission-slot key kept alongside a
// dimension's failure counter (see Admit).
const loginThrottleTrialSuffix = ":trial"

func newCacheLoginThrottler(store contract.CacheStore, maxAttempts, identifierMaxAttempts, ipMaxAttempts int, decay time.Duration) *cacheLoginThrottler {
	if maxAttempts <= 0 {
		maxAttempts = defaultLoginThrottleMaxAttempts
	}
	if identifierMaxAttempts <= 0 {
		identifierMaxAttempts = defaultLoginThrottleIdentifierMaxAttempts
	}
	if ipMaxAttempts <= 0 {
		ipMaxAttempts = defaultLoginThrottleIPMaxAttempts
	}
	if decay <= 0 {
		decay = defaultLoginThrottleDecay
	}
	t := &cacheLoginThrottler{
		maxAttempts:           int64(maxAttempts),
		identifierMaxAttempts: int64(identifierMaxAttempts),
		ipMaxAttempts:         int64(ipMaxAttempts),
		decay:                 decay,
		delayBase:             auth.DefaultIdentifierDelay,
		delayMax:              auth.DefaultIdentifierDelayMax,
	}
	t.setStore(store)
	return t
}

// throttleStore boxes the throttler's cache store for its atomic pointer.
type throttleStore struct{ contract.CacheStore }

// setStore points the throttler at store; nil throttles nothing.
func (t *cacheLoginThrottler) setStore(store contract.CacheStore) {
	t.store.Store(&throttleStore{store})
}

// cache returns the store the throttler counts in now, or nil.
func (t *cacheLoginThrottler) cache() contract.CacheStore {
	if t == nil {
		return nil
	}
	ref := t.store.Load()
	if ref == nil {
		return nil
	}
	return ref.CacheStore
}

// withDelay sets the progressive-delay base and ceiling. A base <= 0
// disables the delay; a ceiling <= 0 falls back to
// auth.DefaultIdentifierDelayMax; a ceiling below base is raised to base.
func (t *cacheLoginThrottler) withDelay(base, ceiling time.Duration) *cacheLoginThrottler {
	if base < 0 {
		base = 0
	}
	if ceiling <= 0 {
		ceiling = auth.DefaultIdentifierDelayMax
	}
	if ceiling < base {
		ceiling = base
	}
	t.delayBase = base
	t.delayMax = ceiling
	return t
}

// limitFor selects the attempt cap for a key by its dimension prefix
// (see auth.ThrottleKeys). The identifier/IP prefixes both start with
// the pair prefix, so they must be checked first; unprefixed keys
// (custom callers) fall back to the pair cap.
func (t *cacheLoginThrottler) limitFor(key string) int64 {
	switch {
	case strings.HasPrefix(key, auth.ThrottleKeyIdentifierPrefix):
		return t.identifierMaxAttempts
	case strings.HasPrefix(key, auth.ThrottleKeyIPPrefix):
		return t.ipMaxAttempts
	default:
		return t.maxAttempts
	}
}

func (t *cacheLoginThrottler) Allow(r *http.Request, key string) bool {
	store := t.cache()
	if store == nil {
		return true
	}
	count, ok := store.GetCtx(requestContext(r), t.cacheKey(key))
	if !ok {
		return true
	}
	return numericCacheValue(count) < t.limitFor(key)
}

// countAttempt records one attempt against key inside the current decay
// window and returns the resulting count. The first attempt of a window
// counts itself with the add-if-absent that seeds the window's TTL, so
// concurrent attempts on a fresh window are counted by the atomic
// increment alone and none resets the count. A later attempt increments
// the live key, which holds at least 1, so its count is at least 2, unless
// the key expired between the add and the increment: every store then
// recreates it from the increment with no expiration (redis INCR, the
// memory and file drivers), which would leave that bucket denying forever.
// A count of 1 that the add did not create is therefore that expiry, and
// the key is re-put under the decay TTL. The re-put can lose an increment
// that lands between the recreate and the re-put, an undercount of at most
// the arrivals in that instant of a real expiry.
func (t *cacheLoginThrottler) countAttempt(ctx context.Context, store contract.CacheStore, key string) (int64, error) {
	cacheKey := t.cacheKey(key)
	added, err := store.AddCtx(ctx, cacheKey, int64(1), t.decay)
	if err == nil && added {
		return 1, nil
	}
	count, err := store.IncrementCtx(ctx, cacheKey, 1)
	if err != nil {
		return 0, err
	}
	if count == 1 {
		_ = store.PutCtx(ctx, cacheKey, int64(1), t.decay)
	}
	return count, nil
}

// Reserve implements contract.LoginReserver: it counts the attempt now
// and reports whether the resulting count is within the key's cap, with
// the progressive delay derived from that same count. Because the count
// each caller sees includes its own reservation, concurrent attempts
// cannot all observe the same remaining capacity. A store error fails
// open, matching Allow.
func (t *cacheLoginThrottler) Reserve(r *http.Request, key string) (bool, time.Duration) {
	store := t.cache()
	if store == nil {
		return true, 0
	}
	count, err := t.countAttempt(requestContext(r), store, key)
	if err != nil {
		return true, 0
	}
	limit := t.limitFor(key)
	if count <= limit {
		return true, 0
	}
	return false, t.delayFor(count - limit)
}

// delayFor returns the bounded progressive delay for an attempt whose
// count exceeds its cap by excess; 0 when the delay is disabled.
func (t *cacheLoginThrottler) delayFor(excess int64) time.Duration {
	if t.delayBase <= 0 {
		return 0
	}
	return auth.ProgressiveDelay(excess, t.delayBase, t.delayMax)
}

// Delay implements contract.LoginDelayer: the bounded exponential delay
// for the key's current excess over its dimension cap, 0 while the key
// is within cap (or the delay is disabled). The stored count includes
// the caller's own Reserve, so the first attempt past the cap (count ==
// cap+1) pays the base delay.
func (t *cacheLoginThrottler) Delay(r *http.Request, key string) time.Duration {
	store := t.cache()
	if store == nil || t.delayBase <= 0 {
		return 0
	}
	count, ok := store.GetCtx(requestContext(r), t.cacheKey(key))
	if !ok {
		return 0
	}
	return t.delayFor(numericCacheValue(count) - t.limitFor(key))
}

// Admit implements contract.LoginAdmitter with an add-if-absent on the
// shared cache store, so the slot spans every app instance sharing it
// (SETNX-with-TTL on redis). A store error fails open, matching Allow:
// a throttle store outage degrades to "no throttling", never to "no
// logins". hold <= 0 admits unconditionally.
func (t *cacheLoginThrottler) Admit(r *http.Request, key string, hold time.Duration) bool {
	store := t.cache()
	if store == nil || hold <= 0 {
		return true
	}
	added, err := store.AddCtx(requestContext(r), t.cacheKey(key)+loginThrottleTrialSuffix, int64(1), hold)
	if err != nil {
		return true
	}
	return added
}

func (t *cacheLoginThrottler) RecordFailure(r *http.Request, key string) {
	store := t.cache()
	if store == nil {
		return
	}
	_, _ = t.countAttempt(requestContext(r), store, key)
}

func (t *cacheLoginThrottler) RecordSuccess(r *http.Request, key string) {
	store := t.cache()
	if store == nil {
		return
	}
	ctx := requestContext(r)
	_ = store.ForgetCtx(ctx, t.cacheKey(key))
	_ = store.ForgetCtx(ctx, t.cacheKey(key)+loginThrottleTrialSuffix)
}

func (t *cacheLoginThrottler) cacheKey(key string) string {
	return loginThrottleCachePrefix + key
}

func requestContext(r *http.Request) context.Context {
	if r == nil {
		return context.Background()
	}
	return r.Context()
}

func numericCacheValue(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		parsed, _ := strconv.ParseInt(n, 10, 64)
		return parsed
	default:
		return 0
	}
}

func configuredLoginThrottleLimit(envVar string, fallback int) int {
	raw := os.Getenv(envVar)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func configuredLoginThrottleMaxAttempts() int {
	return configuredLoginThrottleLimit("AUTH_LOGIN_MAX_ATTEMPTS", defaultLoginThrottleMaxAttempts)
}

func configuredLoginThrottleIdentifierMaxAttempts() int {
	return configuredLoginThrottleLimit("AUTH_LOGIN_MAX_ATTEMPTS_PER_IDENTIFIER", defaultLoginThrottleIdentifierMaxAttempts)
}

func configuredLoginThrottleIPMaxAttempts() int {
	return configuredLoginThrottleLimit("AUTH_LOGIN_MAX_ATTEMPTS_PER_IP", defaultLoginThrottleIPMaxAttempts)
}

// configuredLoginThrottleDuration parses a duration env var ("2s",
// "500ms", or bare seconds). Unset returns fallback; "0" returns 0 so
// operators can disable a delay explicitly; malformed or negative
// input returns fallback.
func configuredLoginThrottleDuration(envVar string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(envVar))
	if raw == "" {
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil && d >= 0 {
		return d
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}

func configuredLoginThrottleIdentifierDelay() time.Duration {
	return configuredLoginThrottleDuration("AUTH_LOGIN_IDENTIFIER_DELAY", auth.DefaultIdentifierDelay)
}

func configuredLoginThrottleIdentifierDelayMax() time.Duration {
	return configuredLoginThrottleDuration("AUTH_LOGIN_IDENTIFIER_DELAY_MAX", auth.DefaultIdentifierDelayMax)
}

func configuredLoginThrottleDecay() time.Duration {
	raw := os.Getenv("AUTH_LOGIN_DECAY")
	if raw == "" {
		return defaultLoginThrottleDecay
	}
	d, err := time.ParseDuration(raw)
	if err == nil && d > 0 {
		return d
	}
	seconds, err := strconv.Atoi(raw)
	if err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return defaultLoginThrottleDecay
}

func installLoginThrottler(manager *auth.Manager, cm cache.CacheManager, log contract.Logger) *cacheLoginThrottler {
	if manager == nil || cm == nil {
		return nil
	}

	store, err := cm.DefaultStore()
	if err != nil || store == nil {
		if log != nil {
			log.Warn(
				"velocity/auth: cache default store unavailable; falling back to no-op login throttler; Scheme.Attempt brute-force protection will NOT work",
				"error", err,
			)
		}
		return nil
	}

	t := newCacheLoginThrottler(
		store,
		configuredLoginThrottleMaxAttempts(),
		configuredLoginThrottleIdentifierMaxAttempts(),
		configuredLoginThrottleIPMaxAttempts(),
		configuredLoginThrottleDecay(),
	).withDelay(
		configuredLoginThrottleIdentifierDelay(),
		configuredLoginThrottleIdentifierDelayMax(),
	)
	manager.SetLoginThrottler(t)
	return t
}

// rebindLoginThrottler moves the login throttler New installed to the
// default store of the cache Services.Cache holds now. The throttler is
// the framework's own value: one a module gave the auth manager instead
// is not touched. A cache with no default store leaves login attempts
// unthrottled, with the warning New gives in that case.
func rebindLoginThrottler(a *App, _, cur ownedSet) (func(), error) {
	t := a.loginThrottler
	if t == nil {
		return nil, nil
	}
	var store contract.CacheStore
	var err error
	if cm, _ := cur[fieldCache].(cache.CacheManager); cm != nil {
		store, err = cm.DefaultStore()
	}
	return func() {
		if nilval.Is(store) {
			store = nil
			fallbacklog.Write(a.Log, func(l contract.Logger) {
				l.Warn("velocity/auth: cache default store unavailable; login attempts are no longer throttled; Scheme.Attempt brute-force protection will NOT work", "error", err)
			})
		}
		t.setStore(store)
	}, nil
}
