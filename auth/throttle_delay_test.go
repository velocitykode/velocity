package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

func TestProgressiveDelay(t *testing.T) {
	const base, max = 100 * time.Millisecond, time.Second
	cases := []struct {
		name   string
		excess int64
		base   time.Duration
		max    time.Duration
		want   time.Duration
	}{
		{name: "under cap", excess: 0, base: base, max: max, want: 0},
		{name: "negative excess", excess: -3, base: base, max: max, want: 0},
		{name: "first over-cap attempt pays base", excess: 1, base: base, max: max, want: base},
		{name: "second doubles", excess: 2, base: base, max: max, want: 2 * base},
		{name: "fourth is 8x", excess: 4, base: base, max: max, want: 8 * base},
		{name: "ceiling clamps", excess: 5, base: base, max: max, want: max},
		{name: "far past ceiling stays clamped", excess: 500, base: base, max: max, want: max},
		{name: "disabled base", excess: 3, base: 0, max: max, want: 0},
		{name: "negative base", excess: 3, base: -time.Second, max: max, want: 0},
		{name: "zero max falls back to default ceiling", excess: 60, base: base, max: 0, want: DefaultIdentifierDelayMax},
		{name: "base above max returns max", excess: 1, base: 2 * time.Second, max: max, want: max},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProgressiveDelay(tc.excess, tc.base, tc.max); got != tc.want {
				t.Fatalf("ProgressiveDelay(%d, %v, %v) = %v, want %v", tc.excess, tc.base, tc.max, got, tc.want)
			}
		})
	}
}

// TestProgressiveDelay_NeverExceedsMaxOrOverflows sweeps the excess range
// past the shift overflow boundary so no input can wrap a time.Duration.
func TestProgressiveDelay_NeverExceedsMaxOrOverflows(t *testing.T) {
	for excess := int64(1); excess <= 200; excess++ {
		got := ProgressiveDelay(excess, time.Second, DefaultIdentifierDelayMax)
		if got <= 0 || got > DefaultIdentifierDelayMax {
			t.Fatalf("excess %d: delay %v outside (0, %v]", excess, got, DefaultIdentifierDelayMax)
		}
	}
}

type fixedDelayThrottler struct {
	NoopLoginThrottler
	delay time.Duration
}

func (f fixedDelayThrottler) Delay(*http.Request, string) time.Duration { return f.delay }

func TestIdentifierDelay(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	cases := []struct {
		name      string
		throttler contract.LoginThrottler
		want      time.Duration
	}{
		{name: "nil throttler", throttler: nil, want: 0},
		{name: "throttler without LoginDelayer pays fixed default", throttler: NoopLoginThrottler{}, want: DefaultIdentifierDelay},
		{name: "delayer answer honoured", throttler: fixedDelayThrottler{delay: 250 * time.Millisecond}, want: 250 * time.Millisecond},
		{name: "delayer opt-out", throttler: fixedDelayThrottler{delay: 0}, want: 0},
		{name: "negative delayer answer clamped", throttler: fixedDelayThrottler{delay: -time.Second}, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IdentifierDelay(tc.throttler, r, ThrottleKeyIdentifierPrefix+"abc"); got != tc.want {
				t.Fatalf("IdentifierDelay = %v, want %v", got, tc.want)
			}
		})
	}
}
