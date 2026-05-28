// Package nonce provides default NonceStore drivers for webhook.Verifier.
//
// The Memory driver in this file is a process-local store backed by a
// sync.RWMutex-protected map with a background TTL sweeper goroutine. It
// is safe for concurrent use, recovers from panics in the sweep loop on a
// per-tick basis (so a transient bug never silently disables nonce
// expiry), and is appropriate for development, single-process deployments,
// or tests. Multi-process deployments that need replay protection across
// replicas should ship a Redis or database-backed driver implementing the
// webhook.NonceStore interface.
package nonce

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/velocitykode/velocity/async"
)

// Memory is an in-process NonceStore. The zero value is not usable; call
// NewMemory to construct one.
type Memory struct {
	mu        sync.RWMutex
	items     map[string]time.Time // nonce -> expiry (UTC)
	stopOnce  sync.Once
	stopCh    chan struct{}
	stoppedCh chan struct{}
	now       func() time.Time // test seam; nil means time.Now
	// panicMu guards onPanic on a separate mutex from m.mu. If collect()
	// panicked while holding m.mu, the deferred recover would deadlock if
	// it had to acquire m.mu to read the handler.
	panicMu sync.RWMutex
	onPanic func(any)
}

// NewMemory returns a Memory NonceStore with a background sweep that runs
// every interval. The sweep removes entries whose expiry has passed. Pass
// a non-positive interval to disable the sweep (entries still expire on
// read but stale records accumulate until the process restarts).
//
// The returned store must be closed with Close to stop the sweep goroutine.
func NewMemory(interval time.Duration) *Memory {
	m := &Memory{
		items:     make(map[string]time.Time),
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
	}
	if interval > 0 {
		// sweep() carries its own per-tick recover so a transient
		// panic does not kill replay protection (the inner recover
		// is scoped to tickWithRecover, not the loop). async.Go
		// wraps the whole sweep call in an outer panic-safe shell:
		// the two layers compose, the inner recover keeps the loop
		// alive and the outer recover catches any future panic
		// raised outside tickWithRecover (today there is none).
		async.Go(func() { m.sweep(interval) })
	} else {
		// No sweep goroutine: stoppedCh signals immediately.
		close(m.stoppedCh)
	}
	return m
}

// SetOnPanic installs a panic observer for the sweep goroutine. The handler
// runs inside the deferred recover; do not panic from it.
func (m *Memory) SetOnPanic(fn func(any)) {
	m.panicMu.Lock()
	m.onPanic = fn
	m.panicMu.Unlock()
}

// nowFn returns the configured time source or wall-clock time.
func (m *Memory) nowFn() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// CheckAndMark atomically reports whether nonce was already present (and
// unexpired) and, if not, records it with the supplied ttl. The check and
// mark happen under a single write lock so two concurrent verifications of
// the same payload cannot both observe alreadySeen=false.
//
// A ttl <= 0 stores the nonce with an immediate-past expiry; a subsequent
// CheckAndMark of the same nonce will then report alreadySeen=false again.
func (m *Memory) CheckAndMark(_ context.Context, nonce string, ttl time.Duration) (bool, error) {
	now := m.nowFn()
	m.mu.Lock()
	defer m.mu.Unlock()
	if exp, ok := m.items[nonce]; ok {
		if !now.After(exp) {
			// Present and unexpired: already seen, do not refresh expiry.
			return true, nil
		}
		// Present but expired: fall through to record a fresh entry.
	}
	m.items[nonce] = now.Add(ttl)
	return false, nil
}

// seen reports whether nonce is present and unexpired. Exposed only as a
// package-private helper for tests; callers must use CheckAndMark to avoid
// TOCTOU races. The clock read happens under the lock so the comparison
// cannot use a stale exp captured before another goroutine refreshed it.
func (m *Memory) seen(nonce string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	exp, ok := m.items[nonce]
	if !ok {
		return false
	}
	return !m.nowFn().After(exp)
}

// Len reports the current number of stored nonces (including any expired
// entries that have not been swept yet). Primarily useful for tests.
func (m *Memory) Len() int {
	m.mu.RLock()
	n := len(m.items)
	m.mu.RUnlock()
	return n
}

// Close stops the background sweep goroutine. Subsequent calls are no-ops.
// Close honours the context deadline only when a sweep is active; for an
// already-stopped store it returns immediately.
func (m *Memory) Close(ctx context.Context) error {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
	select {
	case <-m.stoppedCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sweep removes expired entries every interval until stopCh is closed. The
// loop is panic-recovered per tick: any recovered value is reported via
// onPanic (if set) and the loop continues so a transient bug does not
// silently disable nonce expiry. The recover is deliberately scoped to a
// single iteration; placing it at function-level (outside the loop) would
// cause a single panic to terminate the sweep goroutine for the lifetime
// of the process and silently degrade replay protection.
func (m *Memory) sweep(interval time.Duration) {
	defer close(m.stoppedCh)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-t.C:
			m.tickWithRecover()
		}
	}
}

// tickWithRecover runs one sweep pass with a deferred recover scoped to
// this single iteration. Any recovered panic is reported via onPanic so
// observers (e.g. logger, metrics) can surface the failure.
func (m *Memory) tickWithRecover() {
	defer func() {
		if r := recover(); r != nil {
			// Read onPanic on its own mutex so a panic from inside collect()
			// (which holds m.mu) cannot deadlock the recover.
			m.panicMu.RLock()
			h := m.onPanic
			m.panicMu.RUnlock()
			if h != nil {
				// Wrap so handlers can type-assert to error if they want.
				h(fmt.Errorf("webhook/nonce: sweep panic: %v", r))
			}
		}
	}()
	m.collect()
}

// collect removes every entry whose expiry has passed.
func (m *Memory) collect() {
	now := m.nowFn()
	m.mu.Lock()
	for k, exp := range m.items {
		if now.After(exp) {
			delete(m.items, k)
		}
	}
	m.mu.Unlock()
}
