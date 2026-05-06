package webhook

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

func TestRetryPolicy_Exponential_MonotonicMean(t *testing.T) {
	t.Parallel()

	p := DefaultRetryPolicy
	// Use a wide-enough cap so growth is unconstrained for the first
	// several attempts.
	p.Cap = time.Hour
	p.MaxAttempts = 100

	const samples = 5000
	means := make([]float64, 6)
	for attempt := 0; attempt < len(means); attempt++ {
		var sum float64
		for i := 0; i < samples; i++ {
			d, ok := p.Next(attempt)
			if !ok {
				t.Fatalf("Next(%d) reported !ok", attempt)
			}
			sum += float64(d)
		}
		means[attempt] = sum / float64(samples)
	}

	for i := 1; i < len(means); i++ {
		if means[i] <= means[i-1] {
			t.Fatalf("expected monotonically increasing mean delay, got %v", means)
		}
		// Mean should be roughly Factor*previous within wide bounds (jitter
		// is symmetric so the mean equals the un-jittered delay in the
		// limit). Accept anything between 1.5x and 2.5x for factor=2.
		ratio := means[i] / means[i-1]
		if ratio < 1.5 || ratio > 2.5 {
			t.Fatalf("ratio at attempt %d off: %.2f (means=%v)", i, ratio, means)
		}
	}
}

func TestRetryPolicy_Jitter_Bounded(t *testing.T) {
	t.Parallel()

	const J = 0.25
	p := RetryPolicy{
		BaseDelay:   200 * time.Millisecond,
		Factor:      2,
		MaxAttempts: 20,
		Jitter:      J,
		Cap:         10 * time.Minute,
	}
	for attempt := 0; attempt < 10; attempt++ {
		// Compute the un-jittered base for this attempt.
		raw := float64(p.BaseDelay) * math.Pow(p.Factor, float64(attempt))
		if raw > float64(p.Cap) {
			raw = float64(p.Cap)
		}
		lo := time.Duration(raw * (1 - J))
		hi := time.Duration(raw * (1 + J))
		// Allow a 1ns slack for float -> Duration rounding at the edges.
		lo -= 1
		hi += 1
		for i := 0; i < 1000; i++ {
			d, ok := p.Next(attempt)
			if !ok {
				t.Fatalf("Next(%d) reported !ok", attempt)
			}
			if d < lo || d > hi {
				t.Fatalf("attempt %d sample %d out of [%v, %v]: %v", attempt, i, lo, hi, d)
			}
		}
	}
}

func TestRetryPolicy_StopsAtMax(t *testing.T) {
	t.Parallel()

	p := RetryPolicy{BaseDelay: time.Second, Factor: 2, MaxAttempts: 3, Jitter: 0, Cap: time.Hour}
	for i := 0; i < 3; i++ {
		if _, ok := p.Next(i); !ok {
			t.Fatalf("Next(%d) reported !ok before MaxAttempts", i)
		}
	}
	if d, ok := p.Next(3); ok || d != 0 {
		t.Fatalf("Next(MaxAttempts) expected (0, false), got (%v, %v)", d, ok)
	}
	if d, ok := p.Next(99); ok || d != 0 {
		t.Fatalf("Next beyond MaxAttempts expected (0, false), got (%v, %v)", d, ok)
	}
}

func TestRetryPolicy_ZeroMaxAttempts_NeverRetries(t *testing.T) {
	t.Parallel()

	p := RetryPolicy{BaseDelay: time.Second, Factor: 2, MaxAttempts: 0}
	if d, ok := p.Next(0); ok || d != 0 {
		t.Fatalf("expected (0, false), got (%v, %v)", d, ok)
	}
}

func TestRetryPolicy_NegativeAttemptTreatedAsZero(t *testing.T) {
	t.Parallel()

	p := RetryPolicy{BaseDelay: time.Second, Factor: 2, MaxAttempts: 5, Jitter: 0, Cap: time.Hour}
	d, ok := p.Next(-1)
	if !ok {
		t.Fatalf("expected ok=true for attempt=-1")
	}
	if d != time.Second {
		t.Fatalf("expected ~BaseDelay, got %v", d)
	}
}

func TestRetryPolicy_CapEnforced(t *testing.T) {
	t.Parallel()

	p := RetryPolicy{BaseDelay: time.Second, Factor: 2, MaxAttempts: 30, Jitter: 0, Cap: 4 * time.Second}
	d, ok := p.Next(20) // would explode without the cap
	if !ok {
		t.Fatalf("ok=false")
	}
	if d != 4*time.Second {
		t.Fatalf("expected cap enforced -> 4s, got %v", d)
	}
}

func TestRetryPolicy_CustomRNG(t *testing.T) {
	t.Parallel()

	// Force jitter to its maximum negative end.
	p := RetryPolicy{
		BaseDelay:   time.Second,
		Factor:      1,
		MaxAttempts: 1,
		Jitter:      0.5,
		Cap:         0,
		rng:         func() float64 { return 0 },
	}
	d, ok := p.Next(0)
	if !ok {
		t.Fatalf("ok=false")
	}
	if d != 500*time.Millisecond {
		t.Fatalf("expected 500ms (jitter at -0.5), got %v", d)
	}

	p.rng = func() float64 { return 1 } // jitter at +0.5
	d, ok = p.Next(0)
	if !ok || d != 1500*time.Millisecond {
		t.Fatalf("expected 1500ms (jitter at +0.5), got %v ok=%v", d, ok)
	}
}

func TestRetryPolicy_Defaults_NonZero(t *testing.T) {
	t.Parallel()

	p := DefaultRetryPolicy
	if p.BaseDelay <= 0 || p.MaxAttempts <= 0 || p.Cap <= 0 || p.Factor <= 1 {
		t.Fatalf("DefaultRetryPolicy missing sensible defaults: %+v", p)
	}
}

func TestRetryPolicy_FactorClampedToOne(t *testing.T) {
	t.Parallel()

	p := RetryPolicy{BaseDelay: time.Second, Factor: 0, MaxAttempts: 5, Jitter: 0, Cap: time.Hour}
	d, ok := p.Next(3)
	if !ok || d != time.Second {
		t.Fatalf("expected BaseDelay when Factor<=0, got %v ok=%v", d, ok)
	}
}

func TestRetryPolicy_NegativeBaseDelayClampedToZero(t *testing.T) {
	t.Parallel()

	p := RetryPolicy{BaseDelay: -time.Second, Factor: 2, MaxAttempts: 5, Jitter: 0, Cap: time.Hour}
	d, ok := p.Next(0)
	if !ok || d != 0 {
		t.Fatalf("expected 0 delay, got %v ok=%v", d, ok)
	}
}

func TestRetryPolicy_JitterClamped(t *testing.T) {
	t.Parallel()

	// Jitter > 1 should be clamped to 1.
	p := RetryPolicy{BaseDelay: time.Second, Factor: 1, MaxAttempts: 1, Jitter: 5, Cap: 0, rng: func() float64 { return 0 }}
	d, ok := p.Next(0)
	if !ok {
		t.Fatalf("ok=false")
	}
	// Jitter clamped to 1 with rng=0 -> u=-1 -> delay = base + (-1)*1*base = 0.
	if d != 0 {
		t.Fatalf("expected 0 with clamped jitter at u=-1, got %v", d)
	}

	// Negative jitter should be clamped to 0 (no jitter at all).
	p = RetryPolicy{BaseDelay: time.Second, Factor: 1, MaxAttempts: 1, Jitter: -1, Cap: 0, rng: func() float64 { return 0 }}
	d, ok = p.Next(0)
	if !ok || d != time.Second {
		t.Fatalf("expected exact BaseDelay with negative jitter clamped, got %v ok=%v", d, ok)
	}
}

// Smoke test: the default sample() path returns values in [0, 1). This both
// exercises the production code path and acts as a sanity check on the
// math/rand/v2 import.
func TestRetryPolicy_DefaultSample_InRange(t *testing.T) {
	t.Parallel()

	p := RetryPolicy{}
	for i := 0; i < 100; i++ {
		v := p.sample()
		if v < 0 || v >= 1 {
			t.Fatalf("sample out of [0,1): %v", v)
		}
	}
	// Make sure the package-level rand is seeded usefully (at least two
	// distinct values across 1000 samples).
	seen := map[float64]struct{}{}
	for i := 0; i < 1000; i++ {
		seen[rand.Float64()] = struct{}{}
		if len(seen) >= 2 {
			break
		}
	}
	if len(seen) < 2 {
		t.Fatalf("expected variety from math/rand/v2.Float64")
	}
}
