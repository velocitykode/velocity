package velocity

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
)

func TestCacheLoginThrottler_Delay_ProgressiveOverCap(t *testing.T) {
	const cap = 3
	const base, max = 10 * time.Millisecond, 50 * time.Millisecond
	th := newTestDimensionedLoginThrottler(t, 5, cap, 50).withDelay(base, max)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	key := auth.ThrottleKeyIdentifierPrefix + "victim"

	// Attempts reserved so far (the count includes the caller's own
	// reservation) -> expected Delay for the attempt just reserved.
	want := []time.Duration{0, 0, 0, 0, base, 2 * base, 4 * base, max, max}
	for reserved, exp := range want {
		if got := th.Delay(r, key); got != exp {
			t.Fatalf("after %d reservations: Delay = %v, want %v", reserved, got, exp)
		}
		if allowed := th.Allow(r, key); allowed != (reserved < cap) {
			t.Fatalf("after %d reservations: Allow = %v, want %v", reserved, allowed, reserved < cap)
		}
		if within, _ := th.Reserve(r, key); within != (reserved+1 <= cap) {
			t.Fatalf("reservation %d: Reserve = %v, want %v", reserved+1, within, reserved+1 <= cap)
		}
	}

	th.RecordSuccess(r, key)
	if got := th.Delay(r, key); got != 0 {
		t.Fatalf("Delay after RecordSuccess = %v, want 0", got)
	}
}

func TestCacheLoginThrottler_Delay_UsesDimensionCap(t *testing.T) {
	th := newTestDimensionedLoginThrottler(t, 2, 4, 6).withDelay(time.Millisecond, time.Second)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	cases := []struct {
		key string
		cap int
	}{
		{auth.ThrottleKeyPairPrefix + "p", 2},
		{auth.ThrottleKeyIdentifierPrefix + "i", 4},
		{auth.ThrottleKeyIPPrefix + "ip", 6},
	}
	for _, tc := range cases {
		for i := 0; i < tc.cap; i++ {
			if within, _ := th.Reserve(r, tc.key); !within {
				t.Fatalf("%s: reservation %d denied within cap %d", tc.key, i+1, tc.cap)
			}
			if got := th.Delay(r, tc.key); got != 0 {
				t.Fatalf("%s: Delay within cap (%d reserved) = %v, want 0", tc.key, i+1, got)
			}
		}
		if within, _ := th.Reserve(r, tc.key); within {
			t.Fatalf("%s: reservation past cap %d allowed", tc.key, tc.cap)
		}
		if got := th.Delay(r, tc.key); got != time.Millisecond {
			t.Fatalf("%s: Delay for first attempt past cap = %v, want base", tc.key, got)
		}
	}
}

func TestCacheLoginThrottler_Delay_DisabledAndNilSafe(t *testing.T) {
	th := newTestDimensionedLoginThrottler(t, 5, 1, 50).withDelay(0, time.Second)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	key := auth.ThrottleKeyIdentifierPrefix + "victim"
	th.RecordFailure(r, key)
	th.RecordFailure(r, key)
	if got := th.Delay(r, key); got != 0 {
		t.Fatalf("Delay with base 0 = %v, want 0 (disabled)", got)
	}

	var nilTh *cacheLoginThrottler
	if got := nilTh.Delay(r, key); got != 0 {
		t.Fatalf("nil throttler Delay = %v, want 0", got)
	}
	if got := (&cacheLoginThrottler{}).Delay(nil, key); got != 0 {
		t.Fatalf("storeless throttler Delay = %v, want 0", got)
	}
}

func TestCacheLoginThrottler_WithDelay_Clamps(t *testing.T) {
	th := newTestLoginThrottler(t, 5)
	cases := []struct {
		name      string
		base, max time.Duration
		wantBase  time.Duration
		wantMax   time.Duration
	}{
		{"defaults", auth.DefaultIdentifierDelay, auth.DefaultIdentifierDelayMax, auth.DefaultIdentifierDelay, auth.DefaultIdentifierDelayMax},
		{"negative base disables", -1, time.Second, 0, time.Second},
		{"zero max falls back", time.Second, 0, time.Second, auth.DefaultIdentifierDelayMax},
		{"max below base raised", 5 * time.Second, time.Second, 5 * time.Second, 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			th.withDelay(tc.base, tc.max)
			if th.delayBase != tc.wantBase || th.delayMax != tc.wantMax {
				t.Fatalf("withDelay(%v, %v) = (%v, %v), want (%v, %v)", tc.base, tc.max, th.delayBase, th.delayMax, tc.wantBase, tc.wantMax)
			}
		})
	}
}

func TestConfiguredLoginThrottleDuration(t *testing.T) {
	const envVar = "VELOCITY_TEST_THROTTLE_DURATION"
	fallback := 7 * time.Second
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", fallback},
		{"2s", 2 * time.Second},
		{"500ms", 500 * time.Millisecond},
		{"3", 3 * time.Second},
		{"0", 0},
		{"-1", fallback},
		{"-2s", fallback},
		{"garbage", fallback},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv(envVar, tc.raw)
			if got := configuredLoginThrottleDuration(envVar, fallback); got != tc.want {
				t.Fatalf("configuredLoginThrottleDuration(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestInstallLoginThrottler_HonoursDelayEnv(t *testing.T) {
	t.Setenv("AUTH_LOGIN_IDENTIFIER_DELAY", "250ms")
	t.Setenv("AUTH_LOGIN_IDENTIFIER_DELAY_MAX", "2s")
	manager := auth.NewManager()
	scheme := &fakeLoginThrottlerScheme{}
	manager.RegisterScheme("web", scheme)
	installLoginThrottler(manager, newMemoryCacheManager(), nil)
	th, ok := scheme.throttler.(*cacheLoginThrottler)
	if !ok {
		t.Fatalf("installed throttler is %T, want *cacheLoginThrottler", scheme.throttler)
	}
	if th.delayBase != 250*time.Millisecond || th.delayMax != 2*time.Second {
		t.Fatalf("delay = (%v, %v), want (250ms, 2s)", th.delayBase, th.delayMax)
	}
}
