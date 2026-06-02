package velocity

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
)

const (
	defaultLoginThrottleMaxAttempts = 5
	defaultLoginThrottleDecay       = 60 * time.Second
	loginThrottleCachePrefix        = "velocity:auth:login:"
)

type cacheLoginThrottler struct {
	store       contract.CacheStore
	maxAttempts int64
	decay       time.Duration
}

func newCacheLoginThrottler(store contract.CacheStore, maxAttempts int, decay time.Duration) *cacheLoginThrottler {
	if maxAttempts <= 0 {
		maxAttempts = defaultLoginThrottleMaxAttempts
	}
	if decay <= 0 {
		decay = defaultLoginThrottleDecay
	}
	return &cacheLoginThrottler{
		store:       store,
		maxAttempts: int64(maxAttempts),
		decay:       decay,
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
	return numericCacheValue(count) < t.maxAttempts
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

func configuredLoginThrottleMaxAttempts() int {
	raw := os.Getenv("AUTH_LOGIN_MAX_ATTEMPTS")
	if raw == "" {
		return defaultLoginThrottleMaxAttempts
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultLoginThrottleMaxAttempts
	}
	return n
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
		configuredLoginThrottleDecay(),
	))
}
