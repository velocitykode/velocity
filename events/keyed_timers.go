package events

import (
	"sync"
	"time"
)

// keyedTimers holds at most one pending delivery per key, each armed on a
// timer: the debouncing and coalescing dispatchers are built on it.
//
// Every schedule numbers its timer (a generation). A timer that fires
// claims its key's pending delivery only while that delivery still carries
// its number, removing it under the lock, and then delivers with the lock
// released. So a timer that fired but was replaced before it claimed (its
// Stop came too late) delivers nothing, the replacement keeps its full
// window, and a delivery never runs under the lock: a listener that
// schedules the same key again leaves a new pending delivery, which Stop
// can still cancel.
type keyedTimers[V any] struct {
	mu      sync.Mutex
	gen     uint64
	pending map[string]*keyedTimer[V]
}

// keyedTimer is one pending delivery: its generation, its timer, the
// value it delivers, and the callback its timer runs.
type keyedTimer[V any] struct {
	gen   uint64
	timer *time.Timer
	value V
	fired func()
}

// schedule (re)arms key to run fire after delay. update receives the
// pending value (had is false when none is pending) and returns the value
// the delivery carries; it runs under the lock, so it must not call user
// code. A pending timer for key is stopped and superseded.
func (k *keyedTimers[V]) schedule(key string, delay time.Duration, update func(old V, had bool) V, fire func(V)) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var old V
	prev, had := k.pending[key]
	if had {
		prev.timer.Stop()
		old = prev.value
	}
	k.gen++
	gen := k.gen
	entry := &keyedTimer[V]{gen: gen, value: update(old, had)}
	if k.pending == nil {
		k.pending = make(map[string]*keyedTimer[V])
	}
	k.pending[key] = entry
	entry.fired = func() {
		if v, ok := k.claim(key, gen); ok {
			fire(v)
		}
	}
	entry.timer = time.AfterFunc(delay, entry.fired)
}

// claim removes and returns key's pending value when it is still the one
// generation gen armed.
func (k *keyedTimers[V]) claim(key string, gen uint64) (V, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	entry, ok := k.pending[key]
	if !ok || entry.gen != gen {
		var zero V
		return zero, false
	}
	delete(k.pending, key)
	return entry.value, true
}

// cancel stops and removes key's pending delivery, if any.
func (k *keyedTimers[V]) cancel(key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if entry, ok := k.pending[key]; ok {
		entry.timer.Stop()
		delete(k.pending, key)
	}
}

// stopAll stops and removes every pending delivery. A timer that already
// fired but has not claimed its delivery finds nothing to claim.
func (k *keyedTimers[V]) stopAll() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, entry := range k.pending {
		entry.timer.Stop()
	}
	k.pending = nil
}

// count returns how many deliveries are pending.
func (k *keyedTimers[V]) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.pending)
}

// peek returns key's pending value, if any.
func (k *keyedTimers[V]) peek(key string) (V, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	entry, ok := k.pending[key]
	if !ok {
		var zero V
		return zero, false
	}
	return entry.value, true
}
