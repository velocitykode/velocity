package velocity

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
)

const (
	// Per-dimension defaults. The pair cap stays tight (5 wrong
	// passwords for one account from one IP). The identifier and IP
	// caps are deliberately looser: they aggregate unrelated traffic
	// (one account across all IPs, one IP across all accounts), so a
	// tight cap would let an attacker lock out a victim's account or a
	// shared NAT egress cheaply. They exist to cap distributed brute
	// force and password spraying, not to replace the pair limit.
	defaultLoginThrottleMaxAttempts           = 5
	defaultLoginThrottleIdentifierMaxAttempts = 20
	defaultLoginThrottleIPMaxAttempts         = 50
	defaultLoginThrottleDecay                 = 60 * time.Second
	loginThrottleCachePrefix                  = "velocity:auth:login:"
)

type cacheLoginThrottler struct {
	store contract.CacheStore
	// maxAttempts caps the (identifier, IP) pair dimension and is the
	// fallback for keys carrying no recognised dimension prefix.
	maxAttempts           int64
	identifierMaxAttempts int64
	ipMaxAttempts         int64
	decay                 time.Duration
}

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
	return &cacheLoginThrottler{
		store:                 store,
		maxAttempts:           int64(maxAttempts),
		identifierMaxAttempts: int64(identifierMaxAttempts),
		ipMaxAttempts:         int64(ipMaxAttempts),
		decay:                 decay,
	}
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
	if t == nil || t.store == nil {
		return true
	}
	count, ok := t.store.GetCtx(requestContext(r), t.cacheKey(key))
	if !ok {
		return true
	}
	return numericCacheValue(count) < t.limitFor(key)
}

func (t *cacheLoginThrottler) RecordFailure(r *http.Request, key string) {
	if t == nil || t.store == nil {
		return
	}
	ctx := requestContext(r)
	key = t.cacheKey(key)
	_, _ = t.store.AddCtx(ctx, key, int64(0), t.decay)
	_, _ = t.store.IncrementCtx(ctx, key, 1)
}

func (t *cacheLoginThrottler) RecordSuccess(r *http.Request, key string) {
	if t == nil || t.store == nil {
		return
	}
	_ = t.store.ForgetCtx(requestContext(r), t.cacheKey(key))
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

func installLoginThrottler(manager *auth.Manager, cm cache.CacheManager, log installerLogger) {
	if manager == nil || cm == nil {
		return
	}

	store, err := cm.DefaultStore()
	if err != nil || store == nil {
		if log != nil {
			log.Warn(
				"velocity/auth: cache default store unavailable; falling back to no-op login throttler; guard.Attempt brute-force protection will NOT work",
				"error", err,
			)
		}
		return
	}

	manager.SetLoginThrottler(newCacheLoginThrottler(
		store,
		configuredLoginThrottleMaxAttempts(),
		configuredLoginThrottleIdentifierMaxAttempts(),
		configuredLoginThrottleIPMaxAttempts(),
		configuredLoginThrottleDecay(),
	))
}
