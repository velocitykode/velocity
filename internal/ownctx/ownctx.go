package ownctx

import (
	"context"
	"time"

	"github.com/velocitykode/velocity/internal/tracekeys"
)

// Bridge returns a context the framework owns that ends when ctx ends:
// its Done is ctx's Done channel and its deadline ctx's deadline. Call it
// before taking the lock: it calls ctx's Done, Deadline and Value (for the
// correlation ids), which are user code. A nil ctx, and a ctx that can
// never end and carries no ids (context.Background), return
// context.Background.
func Bridge(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	done := ctx.Done()
	deadline, hasDeadline := ctx.Deadline()
	carried := readIDs(ctx)
	if done == nil && !hasDeadline && carried.none() {
		return context.Background()
	}
	return &owned{done: done, deadline: deadline, hasDeadline: hasDeadline, ids: carried}
}

// Detached returns a context the framework owns that never ends and
// carries ctx's correlation ids: for work that must land even when the
// caller's context ends, bounded by a timeout the caller adds to the
// result. Call it before taking the lock, as Bridge. A nil ctx, and a ctx
// that carries no ids, return context.Background.
func Detached(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	carried := readIDs(ctx)
	if carried.none() {
		return context.Background()
	}
	return &owned{ids: carried}
}

// ids are the correlation ids an owned context answers.
type ids struct {
	request, trace, span, parent any
}

// none reports whether ctx answered no id. Each is compared with nil
// alone, never with ==, so a value of a type that cannot be compared
// does not panic.
func (i ids) none() bool {
	return i.request == nil && i.trace == nil && i.span == nil && i.parent == nil
}

// owned is the context Bridge and Detached return.
type owned struct {
	done        <-chan struct{}
	deadline    time.Time
	hasDeadline bool
	ids         ids
	// held is the Held of a context built by Hold or HoldDetached, nil
	// otherwise.
	held *Held
}

// readIDs reads the correlation ids from ctx, as ctx answers them (a
// request id held lazily is generated here).
func readIDs(ctx context.Context) ids {
	return ids{
		request: ctx.Value(tracekeys.RequestID),
		trace:   ctx.Value(tracekeys.TraceID),
		span:    ctx.Value(tracekeys.SpanID),
		parent:  ctx.Value(tracekeys.ParentID),
	}
}

func (o *owned) Deadline() (time.Time, bool) { return o.deadline, o.hasDeadline }

func (o *owned) Done() <-chan struct{} { return o.done }

func (o *owned) Err() error {
	if o.done == nil {
		return nil
	}
	select {
	case <-o.done:
	default:
		return nil
	}
	if o.hasDeadline && !time.Now().Before(o.deadline) {
		return context.DeadlineExceeded
	}
	return context.Canceled
}

func (o *owned) Value(key any) any {
	switch key {
	case tracekeys.RequestID:
		return o.ids.request
	case tracekeys.TraceID:
		return o.ids.trace
	case tracekeys.SpanID:
		return o.ids.span
	case tracekeys.ParentID:
		return o.ids.parent
	}
	return nil
}

// String names the context for fmt, without calling the caller's.
func (o *owned) String() string { return "ownctx.owned" }
