package webhook

import (
	"math"
	"math/rand/v2"
	"time"
)

// RetryPolicy describes an exponential-backoff schedule with bounded uniform
// jitter and a hard attempt cap. The zero value is not useful; callers should
// either populate fields explicitly or copy DefaultRetryPolicy.
//
// The schedule for attempt N (0-indexed) is:
//
//	delay = min(BaseDelay * Factor^attempt, Cap)
//	delay = delay + uniform(-Jitter*delay, +Jitter*delay)
//
// Next returns (delay, true) while attempt < MaxAttempts, and (0, false)
// once attempt >= MaxAttempts. Callers should treat the second return as
// "stop retrying" and route the work to a dead-letter sink of their choice.
type RetryPolicy struct {
	// BaseDelay is the delay before the first retry (attempt 0).
	BaseDelay time.Duration
	// Factor is the multiplicative growth per attempt. Values <= 1 disable
	// growth (every retry happens at BaseDelay, still subject to jitter
	// and Cap).
	Factor float64
	// MaxAttempts is the inclusive cap on the attempt count. Once
	// attempt >= MaxAttempts, Next reports shouldRetry=false.
	MaxAttempts int
	// Jitter is the uniform jitter fraction in [0, 1]. A value of 0.2
	// means the returned delay is uniformly distributed in
	// [delay*0.8, delay*1.2]. Values outside [0, 1] are clamped.
	Jitter float64
	// Cap is the maximum exponential delay before jitter is applied.
	// Zero disables the cap.
	Cap time.Duration
	// rng overrides the random source for tests. When nil, math/rand/v2's
	// default ChaCha8-backed global source is used (cryptographically
	// non-predictable for the purpose of jitter).
	rng func() float64
}

// DefaultRetryPolicy is a sensible exponential-backoff policy: 1s base,
// factor 2, 8 attempts max, 20% jitter, capped at 5 minutes.
//
//	attempt 0 -> ~1s
//	attempt 1 -> ~2s
//	attempt 2 -> ~4s
//	...
//	attempt 7 -> ~128s (clamped at 5m once growth exceeds the cap)
var DefaultRetryPolicy = RetryPolicy{
	BaseDelay:   1 * time.Second,
	Factor:      2.0,
	MaxAttempts: 8,
	Jitter:      0.2,
	Cap:         5 * time.Minute,
}

// Next returns the delay before the given attempt and whether the caller
// should retry. attempt is 0-indexed: pass 0 before the first retry, 1
// before the second, etc. Once attempt >= MaxAttempts the method returns
// (0, false).
//
// Negative attempt values are treated as 0. Negative BaseDelay or Cap are
// treated as 0 (no wait, never retry beyond cap).
func (p RetryPolicy) Next(attempt int) (time.Duration, bool) {
	if p.MaxAttempts <= 0 || attempt >= p.MaxAttempts {
		return 0, false
	}
	if attempt < 0 {
		attempt = 0
	}
	base := p.BaseDelay
	if base < 0 {
		base = 0
	}
	factor := p.Factor
	if factor <= 0 {
		factor = 1
	}

	// math.Pow is fine here: attempt is bounded by MaxAttempts (small int)
	// and float precision is irrelevant once we clamp at Cap.
	mult := math.Pow(factor, float64(attempt))
	delayF := float64(base) * mult
	if p.Cap > 0 && delayF > float64(p.Cap) {
		delayF = float64(p.Cap)
	}

	jitter := p.Jitter
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 1 {
		jitter = 1
	}
	if jitter > 0 {
		// Uniform sample in [-1, 1] scaled by jitter*delay.
		u := p.sample()*2 - 1
		delayF += u * jitter * delayF
	}
	if delayF < 0 {
		delayF = 0
	}
	return time.Duration(delayF), true
}

// sample returns a uniform value in [0, 1). Internal seam for test
// determinism. The default uses math/rand/v2's package-level generator,
// which is seeded from OS entropy at package init and is backed by ChaCha8
// (non-predictable enough for jitter; jitter is not security-critical).
func (p RetryPolicy) sample() float64 {
	if p.rng != nil {
		return p.rng()
	}
	return rand.Float64()
}
