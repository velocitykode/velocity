package events

import (
	"context"
	"sync"
	"time"
)

// holderKey is the context key under which a *bufferHolder is stored
// when callers opt in via PrepareBuffer (or WithBuffer). The holder is a
// mutable slot so orm.Manager.Transaction can install a per-transaction
// buffer reachable from the SAME ctx variable the closure captured (the
// fn signature stays unchanged).
type holderKey struct{}

// bufferHolder is a mutable cell carrying the active buffer for a ctx.
// It allows orm.Manager.Transaction to install a buffer after the ctx
// was constructed, while keeping reads cheap and concurrent-safe.
type bufferHolder struct {
	mu  sync.RWMutex
	buf *BufferedDispatcher
}

func (h *bufferHolder) load() *BufferedDispatcher {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.buf
}

func (h *bufferHolder) swap(b *BufferedDispatcher) (prev *BufferedDispatcher) {
	h.mu.Lock()
	defer h.mu.Unlock()
	prev, h.buf = h.buf, b
	return prev
}

// DispatchKind tags a buffered event with the dispatch method the caller
// originally requested. The Flush callback uses the kind to route the
// event back through the matching method on the underlying dispatcher so
// listener semantics like ShouldQueue / async / delay are preserved.
type DispatchKind int

const (
	// KindDispatch corresponds to Dispatcher.Dispatch, the default path
	// where listeners with ShouldQueue may opt in to async delivery.
	KindDispatch DispatchKind = iota
	// KindDispatchNow corresponds to Dispatcher.DispatchNow: synchronous
	// delivery to every listener regardless of ShouldQueue.
	KindDispatchNow
	// KindDispatchAsync corresponds to Dispatcher.DispatchAsync: every
	// listener fires off the request goroutine.
	KindDispatchAsync
	// KindDispatchAfter corresponds to Dispatcher.DispatchAfter: async
	// delivery after the recorded delay.
	KindDispatchAfter
	// KindUntil corresponds to Dispatcher.Until: invoke listeners one by
	// one until one returns a non-nil result.
	KindUntil
)

// String returns a human-readable kind name for diagnostics.
func (k DispatchKind) String() string {
	switch k {
	case KindDispatch:
		return "Dispatch"
	case KindDispatchNow:
		return "DispatchNow"
	case KindDispatchAsync:
		return "DispatchAsync"
	case KindDispatchAfter:
		return "DispatchAfter"
	case KindUntil:
		return "Until"
	default:
		return "unknown"
	}
}

// BufferedEvent carries a buffered event together with the dispatch kind
// and (for KindDispatchAfter) the original delay. It is the value passed
// to FlushFunc on Flush so callers can route the event back through the
// dispatcher method the caller originally requested.
//
// BufferedEvent is intentionally a small value type with read-only
// getters: implementations should not mutate the underlying event during
// flush.
type BufferedEvent struct {
	event interface{}
	kind  DispatchKind
	delay time.Duration
}

// Event returns the user-supplied event payload.
func (e BufferedEvent) Event() interface{} { return e.event }

// Kind returns the DispatchKind that recorded the event.
func (e BufferedEvent) Kind() DispatchKind { return e.kind }

// Delay returns the requested delay for KindDispatchAfter entries; zero
// for every other kind.
func (e BufferedEvent) Delay() time.Duration { return e.delay }

// FlushFunc forwards a buffered entry to the underlying dispatcher,
// respecting the original dispatch kind so ShouldQueue / async / delay
// semantics flow through. Implementations switch on entry.Kind() and call
// the matching method on the wrapped Dispatcher.
//
// Returning a non-nil error stops the flush at that entry; the failing
// entry and every entry after it are returned to the buffer so the caller
// can inspect or retry by calling Flush again.
type FlushFunc func(entry BufferedEvent) error

// BufferedDispatcher records dispatched events instead of forwarding them
// to listeners, deferring delivery until Flush is called. The buffer is
// safe for concurrent use; orm.Manager.Transaction installs one buffer per
// transaction so concurrent transactions never share state.
//
// Buffered events fire if-and-only-if Flush is called: Drop discards them,
// providing transactional dispatch semantics on top of orm transactions.
//
// BufferedDispatcher exposes Dispatch / DispatchNow / DispatchAsync /
// DispatchAfter / Until that record events into the buffer. Each
// recorded entry remembers the dispatch kind (and DispatchAfter delay)
// so Flush can route the event back through the matching method on the
// underlying dispatcher; listeners' ShouldQueue and async semantics are
// therefore preserved across the buffer boundary.
type BufferedDispatcher struct {
	mu       sync.Mutex
	events   []BufferedEvent
	flushFn  FlushFunc
	flushed  bool
	flushing bool
	dropped  bool
	parent   *BufferedDispatcher // non-nil for nested handles
	baseline int                 // events length at nesting time (parent only)
}

// PrepareBuffer attaches a mutable buffer holder to ctx so a later
// orm.Manager.Transaction call can install a per-transaction buffer
// without requiring the user's tx callback to receive a derived ctx.
// Callers MUST use the returned ctx for both Transaction and any
// subsequent events.Buffer(ctx) lookups.
//
// PrepareBuffer is idempotent: if ctx already carries a holder, the
// same ctx is returned unchanged.
func PrepareBuffer(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(holderKey{}).(*bufferHolder); ok {
		return ctx
	}
	return context.WithValue(ctx, holderKey{}, &bufferHolder{})
}

// Buffer returns the *BufferedDispatcher attached to ctx (typically
// installed by orm.Manager.Transaction inside a PrepareBuffer-prepared
// ctx). If none is attached, a fresh standalone buffer is returned;
// events recorded on a standalone buffer are silently discarded on Flush
// because no underlying dispatcher is wired. Buffer is safe to call from
// any goroutine.
func Buffer(ctx context.Context) *BufferedDispatcher {
	if b := lookupBuffer(ctx); b != nil {
		return b
	}
	return &BufferedDispatcher{}
}

// HasBuffer reports whether ctx currently carries a BufferedDispatcher
// (either via WithBuffer or via an active orm.Manager.Transaction).
func HasBuffer(ctx context.Context) bool {
	return lookupBuffer(ctx) != nil
}

// lookupBuffer returns the buffer attached to ctx via the holder slot
// or via direct WithBuffer attachment. Returns nil if neither is set.
func lookupBuffer(ctx context.Context) *BufferedDispatcher {
	if ctx == nil {
		return nil
	}
	if h, ok := ctx.Value(holderKey{}).(*bufferHolder); ok && h != nil {
		if b := h.load(); b != nil {
			return b
		}
	}
	if b, ok := ctx.Value(bufferKey{}).(*BufferedDispatcher); ok && b != nil {
		return b
	}
	return nil
}

// bufferKey is the context key used by WithBuffer to attach a buffer
// directly (without going through a holder). Distinct from holderKey so
// PrepareBuffer-prepared contexts and direct WithBuffer attachments do
// not collide.
type bufferKey struct{}

// WithBuffer attaches a BufferedDispatcher to ctx and returns the derived
// ctx and the buffer. The flushFn is invoked once per buffered event on
// Flush, in dispatch order.
//
// Nested semantics: if ctx already carries a buffer, WithBuffer returns
// the existing OUTER buffer wrapped in a child handle so events buffered
// inside the inner scope flow into the outer scope and are flushed only
// when the outermost commit calls Flush. Inner Drop truncates only events
// emitted past the nesting checkpoint (savepoint semantics); inner Flush
// is a no-op so forwarding remains owned by the outermost scope.
func WithBuffer(ctx context.Context, flushFn FlushFunc) (context.Context, *BufferedDispatcher) {
	if ctx == nil {
		ctx = context.Background()
	}
	if existing := lookupBuffer(ctx); existing != nil {
		existing.mu.Lock()
		baseline := len(existing.events)
		existing.mu.Unlock()
		child := &BufferedDispatcher{parent: existing, baseline: baseline}
		return ctx, child
	}
	buf := &BufferedDispatcher{flushFn: flushFn}
	return context.WithValue(ctx, bufferKey{}, buf), buf
}

// InstallBuffer is the entry orm.Manager.Transaction uses to install a
// per-transaction buffer into a holder slot on ctx. Behaviour:
//
//   - If ctx carries a holder slot (PrepareBuffer was called) and the
//     slot is empty, a fresh buffer is installed and a release func is
//     returned that clears the slot on Transaction exit. Returns the
//     fresh buffer.
//   - If the holder slot is already filled (nested Transaction on the
//     same prepared ctx), a child handle anchored to the parent's
//     current event count is returned (savepoint semantics). The
//     release is a no-op so the outer Transaction retains ownership.
//   - If ctx has no holder, a standalone buffer is returned with a
//     no-op release. Events recorded on it are unreachable via
//     Buffer(ctx) (because no holder), so the caller's tx fn cannot
//     use them; the standalone path exists so Transaction stays
//     correct even when callers forget PrepareBuffer.
func InstallBuffer(ctx context.Context, flushFn FlushFunc) (*BufferedDispatcher, func()) {
	if ctx == nil {
		buf := &BufferedDispatcher{flushFn: flushFn}
		return buf, func() {}
	}
	holder, _ := ctx.Value(holderKey{}).(*bufferHolder)
	if holder == nil {
		// No prepared holder: return a standalone buffer. The user
		// won't be able to reach it via Buffer(ctx), but Transaction
		// proceeds normally.
		buf := &BufferedDispatcher{flushFn: flushFn}
		return buf, func() {}
	}
	if existing := holder.load(); existing != nil {
		existing.mu.Lock()
		baseline := len(existing.events)
		existing.mu.Unlock()
		child := &BufferedDispatcher{parent: existing, baseline: baseline}
		return child, func() {}
	}
	buf := &BufferedDispatcher{flushFn: flushFn}
	holder.swap(buf)
	return buf, func() { holder.swap(nil) }
}

// recordKind appends a typed entry to the buffer (or its parent for nested
// handles) honouring the dropped/flushed/flushing flags.
func (b *BufferedDispatcher) recordKind(event interface{}, kind DispatchKind, delay time.Duration) {
	if event == nil {
		return
	}
	target := b
	if b.parent != nil {
		target = b.parent
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if target.dropped || target.flushed || target.flushing {
		return
	}
	target.events = append(target.events, BufferedEvent{event: event, kind: kind, delay: delay})
}

// Flush forwards buffered events to the underlying dispatcher in order,
// stopping at the first error and returning it. After a successful Flush
// the buffer is marked flushed and further Dispatch calls are no-ops.
//
// Partial-failure semantics: if FlushFunc returns an error for entry N,
// the failing entry plus every entry after it are swapped back into the
// buffer and the buffer is left in a non-flushed, non-flushing state so
// the caller can call Flush again to retry. Successful entries (0..N-1)
// are NOT redelivered. Re-entrant Dispatch calls invoked from a
// FlushFunc are dropped while the flush is in progress.
//
// On a nested (child) handle Flush is a no-op because forwarding is owned
// by the outermost scope.
//
// On panic from FlushFunc the buffer is moved into the flushed terminal
// state (Pending becomes 0) and the panic re-propagates. Panic recovery
// is the caller's responsibility: the buffer guarantees only that its
// own state remains consistent.
func (b *BufferedDispatcher) Flush() error {
	if b.parent != nil {
		return nil
	}
	b.mu.Lock()
	if b.flushed || b.dropped {
		b.mu.Unlock()
		return nil
	}
	if b.flushing {
		// Re-entrant Flush from inside a FlushFunc: silently no-op so
		// the outer Flush retains control of the drain.
		b.mu.Unlock()
		return nil
	}
	b.flushing = true
	pending := b.events
	b.events = nil
	fn := b.flushFn
	b.mu.Unlock()

	if fn == nil {
		// No sink: entries are discarded; buffer transitions to flushed.
		b.mu.Lock()
		b.flushing = false
		b.flushed = true
		b.mu.Unlock()
		return nil
	}

	// On panic (FlushFunc misbehaved) terminate the buffer in flushed state
	// so subsequent Dispatch calls are dropped and Pending reports 0. We do
	// NOT swallow the panic; it propagates after we restore consistent
	// state.
	completed := false
	defer func() {
		if completed {
			return
		}
		if p := recover(); p != nil {
			b.mu.Lock()
			b.flushing = false
			b.flushed = true
			b.events = nil
			b.mu.Unlock()
			panic(p)
		}
	}()

	for i, entry := range pending {
		if err := fn(entry); err != nil {
			// Partial failure: put the failing entry plus the remainder
			// back so a retry can resume. Successful entries (0..i-1)
			// are dropped, they already fired.
			remainder := pending[i:]
			b.mu.Lock()
			// Splice remainder ahead of any events recorded re-entrantly
			// during flush. flushing=true normally blocks re-entry but a
			// nested goroutine could have raced; preserve order anyway.
			if len(b.events) > 0 {
				combined := make([]BufferedEvent, 0, len(remainder)+len(b.events))
				combined = append(combined, remainder...)
				combined = append(combined, b.events...)
				b.events = combined
			} else {
				// Copy to detach from `pending`'s backing array so the
				// caller's original slice can be GC'd alongside the
				// already-fired prefix.
				dup := make([]BufferedEvent, len(remainder))
				copy(dup, remainder)
				b.events = dup
			}
			b.flushing = false
			// flushed stays false so the caller can retry by calling
			// Flush again; record() now accepts new events too.
			b.mu.Unlock()
			completed = true
			return err
		}
	}

	b.mu.Lock()
	b.flushing = false
	b.flushed = true
	b.mu.Unlock()
	completed = true
	return nil
}

// Drop discards buffered events and prevents further recording.
//
// On a nested (child) handle Drop truncates the parent buffer back to the
// baseline captured at WithBuffer time so only events emitted within the
// inner scope are removed; the outer scope remains intact.
func (b *BufferedDispatcher) Drop() {
	if b.parent != nil {
		b.parent.mu.Lock()
		defer b.parent.mu.Unlock()
		if b.baseline < len(b.parent.events) {
			b.parent.events = b.parent.events[:b.baseline]
		}
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dropped = true
	b.events = nil
}

// Pending returns the number of events currently buffered for delivery.
// On nested handles this returns the parent's pending count.
func (b *BufferedDispatcher) Pending() int {
	target := b
	if b.parent != nil {
		target = b.parent
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	return len(target.events)
}

// Dispatch records the event in the buffer for later flush as a
// KindDispatch entry.
func (b *BufferedDispatcher) Dispatch(event interface{}) error {
	b.recordKind(event, KindDispatch, 0)
	return nil
}

// DispatchNow records the event in the buffer for later flush as a
// KindDispatchNow entry.
func (b *BufferedDispatcher) DispatchNow(event interface{}) error {
	b.recordKind(event, KindDispatchNow, 0)
	return nil
}

// DispatchAsync records the event in the buffer for later flush as a
// KindDispatchAsync entry.
func (b *BufferedDispatcher) DispatchAsync(event interface{}) error {
	b.recordKind(event, KindDispatchAsync, 0)
	return nil
}

// DispatchAfter records the event in the buffer for later flush as a
// KindDispatchAfter entry. The delay is preserved on the entry and
// applied by FlushFunc when the buffer drains; it is NOT a wall-clock
// delay starting at the time of Dispatch.
func (b *BufferedDispatcher) DispatchAfter(event interface{}, delay time.Duration) error {
	b.recordKind(event, KindDispatchAfter, delay)
	return nil
}

// Until records the event in the buffer for later flush as a KindUntil
// entry. Until's "first non-nil result" semantics cannot be observed
// from inside a transaction (no listener has run yet), so the buffered
// call always returns (nil, nil); FlushFunc is responsible for routing
// the entry through Dispatcher.Until on flush.
func (b *BufferedDispatcher) Until(event interface{}) (interface{}, error) {
	b.recordKind(event, KindUntil, 0)
	return nil, nil
}
