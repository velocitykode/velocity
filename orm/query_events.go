package orm

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// queryEventQueueSize bounds the number of statement events awaiting delivery.
// A listener slower than the query rate fills the queue; further events are
// counted as dropped rather than allowed to stall a database call.
const queryEventQueueSize = 1024

// pendingEvent is one queued item. A non-nil flush marks a barrier rather than
// an event: the pump closes it once every event queued ahead of it has been
// delivered.
type pendingEvent struct {
	ctx   context.Context
	event Event
	flush chan struct{}
}

// eventPump delivers statement events on a goroutine of its own.
//
// Statement events originate inside a database/sql driver callback, which runs
// under that connection's lock and before the connection returns to the pool.
// Running a listener there is a deadlock: a listener that queries the same
// pool waits for a connection that cannot be freed until the listener returns,
// which with MaxOpenConns=1 never happens. The pump exists so the driver
// callback only ever performs a non-blocking channel send.
//
// Delivery is FIFO. Events are dropped, never blocked on, when the queue is
// full; Manager.DroppedQueryEvents reports the count.
type eventPump struct {
	ch   chan pendingEvent
	quit chan struct{}

	dropped  atomic.Int64
	stopped  atomic.Bool
	stopOnce sync.Once
}

func newEventPump() *eventPump {
	return &eventPump{
		ch:   make(chan pendingEvent, queryEventQueueSize),
		quit: make(chan struct{}),
	}
}

// start launches the delivery goroutine. dispatch is called once per event on
// the pump goroutine.
func (p *eventPump) start(dispatch func(context.Context, Event)) {
	async.Go(func() { p.run(dispatch) })
}

func (p *eventPump) run(dispatch func(context.Context, Event)) {
	for {
		select {
		case item := <-p.ch:
			p.deliver(dispatch, item)
		case <-p.quit:
			// Deliver what is already queued so a stop cannot
			// discard events that were accepted before it.
			for {
				select {
				case item := <-p.ch:
					p.deliver(dispatch, item)
				default:
					return
				}
			}
		}
	}
}

// deliver dispatches one item, recovering from a panicking listener. The
// synchronous path let a listener panic propagate to whoever ran the query;
// here there is no such caller, and letting the panic escape would kill the
// pump and silence every later event.
func (p *eventPump) deliver(dispatch func(context.Context, Event), item pendingEvent) {
	if item.flush != nil {
		close(item.flush)
		return
	}
	defer func() {
		if r := recover(); r != nil {
			_ = panicerr.FromRecovered(r)
		}
	}()
	dispatch(item.ctx, item.event)
}

// enqueue hands an event to the pump. It never blocks: this runs inside a
// driver callback holding a connection.
func (p *eventPump) enqueue(ctx context.Context, ev Event) {
	if p.stopped.Load() {
		return
	}
	select {
	case p.ch <- pendingEvent{ctx: ctx, event: ev}:
	default:
		p.dropped.Add(1)
	}
}

// flush blocks until every event queued before the call has been delivered.
//
// The barrier send is blocking, unlike enqueue, because a dropped barrier
// would report a flush that never happened. That is safe only away from a
// driver callback, which is the only place flush is called from.
func (p *eventPump) flush(ctx context.Context) error {
	if p.stopped.Load() {
		return nil
	}
	done := make(chan struct{})
	select {
	case p.ch <- pendingEvent{flush: done}:
	case <-p.quit:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-p.quit:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// stop drains and shuts the pump down. The channel is never closed, so an
// enqueue racing with stop is discarded rather than panicking.
func (p *eventPump) stop(ctx context.Context) {
	p.stopOnce.Do(func() {
		_ = p.flush(ctx)
		p.stopped.Store(true)
		close(p.quit)
	})
}

// FlushQueryEvents blocks until every query event recorded before the call has
// reached the event dispatcher, or ctx expires.
//
// Statement events are delivered asynchronously (see eventPump), so a listener
// has not necessarily observed a query by the time the call that issued it
// returns. Use this to force delivery at a boundary that needs it: a test
// asserting on dispatched events, or an application draining telemetry before
// exiting. Manager.Shutdown flushes on its own.
//
// Must not be called from an event listener: listeners run on the pump
// goroutine, and waiting there for the pump to drain deadlocks.
func (m *Manager) FlushQueryEvents(ctx context.Context) error {
	p := m.pump.Load()
	if p == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return p.flush(ctx)
}

// DroppedQueryEvents reports how many statement events were discarded because
// the delivery queue was full. A non-zero count means listeners are slower
// than the query rate; the alternative to dropping is stalling queries.
func (m *Manager) DroppedQueryEvents() int64 {
	p := m.pump.Load()
	if p == nil {
		return 0
	}
	return p.dropped.Load()
}
