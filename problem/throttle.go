package problem

import (
	"math/rand/v2"
	"reflect"
	"sync"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// defaultThrottleWindow is the MaxPerWindow period used when a Throttle sets
// a cap but no Window.
const defaultThrottleWindow = time.Minute

// maxThrottleBuckets bounds the bucket map; past it, expired buckets are
// swept on the next insert so a per-value By key cannot grow it without end.
const maxThrottleBuckets = 4096

// throttleBuckets holds the in-process MaxPerWindow counters, keyed by the
// matched rule plus the Throttle.By key.
type throttleBuckets struct {
	mu      sync.Mutex
	buckets map[bucketID]*bucket
	now     func() time.Time
	sample  func() float64
}

type bucketID struct {
	rule any
	by   string
}

type bucket struct {
	start  time.Time
	window time.Duration
	count  int
}

func newThrottleBuckets() *throttleBuckets {
	return &throttleBuckets{
		buckets: make(map[bucketID]*bucket),
		now:     time.Now,
		sample:  rand.Float64,
	}
}

// allow reports whether one more report of err under rule passes the rule's
// Throttle: the Sample draw first, then the MaxPerWindow cap (only a report
// that survives sampling consumes a slot).
func (t *throttleBuckets) allow(rule contract.ThrottleRule, err error) bool {
	th := rule.Throttle
	if th.Sample > 0 && th.Sample < 1 && t.sample() >= th.Sample {
		return false
	}
	if th.MaxPerWindow <= 0 {
		return true
	}
	window := th.Window
	if window <= 0 {
		window = defaultThrottleWindow
	}
	id := bucketID{rule: throttleRuleID(rule)}
	if th.By != nil {
		id.by = th.By(err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	b, ok := t.buckets[id]
	if !ok {
		if len(t.buckets) >= maxThrottleBuckets {
			t.sweep(now)
		}
		b = &bucket{start: now, window: window}
		t.buckets[id] = b
	}
	if now.Sub(b.start) >= b.window {
		b.start, b.window, b.count = now, window, 0
	}
	if b.count >= th.MaxPerWindow {
		return false
	}
	b.count++
	return true
}

// sweep drops every bucket whose window has passed. Callers hold t.mu.
func (t *throttleBuckets) sweep(now time.Time) {
	for id, b := range t.buckets {
		if now.Sub(b.start) >= b.window {
			delete(t.buckets, id)
		}
	}
}

// throttleRuleID identifies a rule's buckets: its Key, or for an anonymous
// rule the code pointer of its matcher.
func throttleRuleID(rule contract.ThrottleRule) any {
	if rule.Key != nil {
		return rule.Key
	}
	return reflect.ValueOf(rule.Match).Pointer()
}
