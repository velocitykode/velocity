package orm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// queryEventQueueSize bounds the number of statement events awaiting delivery.
// A listener slower than the query rate fills the queue; further events are
// dropped, as failed events, rather than allowed to stall a database call.
const queryEventQueueSize = 1024

// ErrQueryEventQueueFull is the failure a dropped statement event is
// recorded with: listeners are slower than the query rate and the delivery
// queue is full, so the event never reached a listener (the alternative
// is stalling queries). The failure policy counts it as a failed event
// (App.FailedEventCount) at once, exactly. Later, off the statement's
// path, it logs the first drop and hands the drop to the failure hook, so
// a hook can tell a drop from a listener failure with errors.Is, and may
// itself use the database. That later part is best-effort: when the hook
// is so slow that the backlog of drops waiting for it is full, a further
// drop is counted but neither logged nor handed to the hook.
var ErrQueryEventQueueFull = errors.New("velocity/orm: query event queue full, dropping event")

// ErrQueryEventsFlushFromPump is returned by Manager.FlushQueryEvents and
// Manager.Shutdown when called from a goroutine the statement-event pump
// runs user code on: an event listener, or the failure hook handed a
// dropped event. Either would wait for that goroutine to finish, which is
// itself, so the call is refused at once instead, and Shutdown changes
// nothing. Flush or shut down from another goroutine.
var ErrQueryEventsFlushFromPump = errors.New("velocity/orm: query events cannot be flushed from a listener or drop hook")

// pendingEvent is one queued item. A non-nil flush marks a barrier rather than
// an event: the pump closes it once every event queued ahead of it has been
// delivered.
type pendingEvent struct {
	ctx   context.Context
	event contract.Event
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
// full. Each listener panic the pump recovers goes to fail, the manager's
// failure policy, on the pump goroutine. Each drop goes to failLater, which
// counts it inside the driver callback without blocking; the rest of the
// policy (the first-drop line and the failure hook, which may log, block,
// or query the same pool) runs on a reporter goroutine of the pump's own,
// after the callback has released its connection and whatever the
// listener is doing. The count is exact; the line and the hook are
// best-effort, skipped for a drop that finds the reporter's backlog full.
type eventPump struct {
	ch      chan pendingEvent
	reports chan func()
	quit    chan struct{}
	// delivered and reported are closed when the delivery and the reporter
	// goroutine return, after draining what was queued before quit.
	delivered chan struct{}
	reported  chan struct{}
	fail      func(ctx context.Context, err error, event any)
	failLater func(ctx context.Context, err error, event any) func()

	stopped  atomic.Bool
	stopOnce sync.Once

	// deliverer and reporter are the goroutine ids of the delivery and
	// reporter goroutines (eventemit.GoroutineID), each stored once by the
	// goroutine itself before it runs any user code, so a flush can tell
	// it was called from one of them.
	deliverer atomic.Uint64
	reporter  atomic.Uint64
}

// newEventPump returns a pump that hands each recovered listener panic to
// fail and each dropped event to failLater, running the report failLater
// returns on its reporter goroutine.
func newEventPump(fail func(ctx context.Context, err error, event any), failLater func(ctx context.Context, err error, event any) func()) *eventPump {
	return &eventPump{
		ch:        make(chan pendingEvent, queryEventQueueSize),
		reports:   make(chan func(), queryEventQueueSize),
		quit:      make(chan struct{}),
		delivered: make(chan struct{}),
		reported:  make(chan struct{}),
		fail:      fail,
		failLater: failLater,
	}
}

// start launches the delivery goroutine and the reporter goroutine.
// dispatch is called once per event on the delivery goroutine.
func (p *eventPump) start(dispatch func(context.Context, contract.Event)) {
	async.Go(func() {
		defer close(p.delivered)
		p.deliverer.Store(eventemit.GoroutineID())
		p.run(dispatch)
	})
	async.Go(func() {
		defer close(p.reported)
		p.reporter.Store(eventemit.GoroutineID())
		p.runReports()
	})
}

// onPumpGoroutine reports whether the caller runs on the delivery or the
// reporter goroutine, where a flush would wait on itself.
func (p *eventPump) onPumpGoroutine() bool {
	id := eventemit.GoroutineID()
	return id == p.deliverer.Load() || id == p.reporter.Load()
}

// runReports runs the drop reports enqueue hands over, in order, until
// stop; it runs those already handed over before returning.
func (p *eventPump) runReports() {
	for {
		select {
		case report := <-p.reports:
			runReport(report)
		case <-p.quit:
			for {
				select {
				case report := <-p.reports:
					runReport(report)
				default:
					return
				}
			}
		}
	}
}

// runReport runs one drop report, containing a panic in it (the hook's own
// panic is recovered and counted by the policy; this covers the logger) so
// the reporter goroutine survives to run the next one.
func runReport(report func()) {
	defer func() { _ = recover() }()
	report()
}

func (p *eventPump) run(dispatch func(context.Context, contract.Event)) {
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
// pump and silence every later event. The recovered panic goes to the
// failure policy like a listener error.
func (p *eventPump) deliver(dispatch func(context.Context, contract.Event), item pendingEvent) {
	if item.flush != nil {
		close(item.flush)
		return
	}
	defer func() {
		if r := recover(); r != nil {
			p.fail(item.ctx, panicerr.FromRecovered(r), item.event)
		}
	}()
	dispatch(item.ctx, item.event)
}

// enqueue hands an event to the pump. It never blocks: this runs inside a
// driver callback holding a connection. A drop is counted here and its
// report handed to the reporter goroutine; when the reporter is as far
// behind as the queue (its hook is slower still), the drop stays counted
// and its line and hook are skipped rather than stall the statement.
func (p *eventPump) enqueue(ctx context.Context, ev contract.Event) {
	if p.stopped.Load() {
		return
	}
	select {
	case p.ch <- pendingEvent{ctx: ctx, event: ev}:
	default:
		if report := p.failLater(ctx, ErrQueryEventQueueFull, ev); report != nil {
			select {
			case p.reports <- report:
			default:
			}
		}
	}
}

// flush blocks until every event queued before the call has been delivered
// and the report of every event dropped before it has run.
//
// The barrier sends are blocking, unlike enqueue, because a dropped barrier
// would report a flush that never happened. That is safe only away from a
// driver callback, which is the only place flush is called from, and away
// from the pump's own goroutines, where it returns
// ErrQueryEventsFlushFromPump without waiting.
func (p *eventPump) flush(ctx context.Context) error {
	if p.onPumpGoroutine() {
		return ErrQueryEventsFlushFromPump
	}
	if p.stopped.Load() {
		return p.awaitExit(ctx)
	}
	done := make(chan struct{})
	if err := await(p, ctx, p.ch, pendingEvent{flush: done}, done); err != nil {
		return err
	}
	reported := make(chan struct{})
	return await(p, ctx, p.reports, func() { close(reported) }, reported)
}

// await sends barrier on ch, then waits for done, or with ctx's error when
// ctx ends first. When the pump stops meanwhile, the barrier may never be
// reached, so it waits for the pump's goroutines to finish draining instead
// (awaitExit): a nil return always means everything admitted before the
// call has been handled.
func await[T any](p *eventPump, ctx context.Context, ch chan T, barrier T, done chan struct{}) error {
	select {
	case ch <- barrier:
	case <-p.quit:
		return p.awaitExit(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-p.quit:
		return p.awaitExit(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// awaitExit waits for a stopped pump's delivery and reporter goroutines to
// return, which they do once they have drained everything queued before
// the stop, or returns ctx's error when ctx ends first.
func (p *eventPump) awaitExit(ctx context.Context) error {
	for _, exited := range []chan struct{}{p.delivered, p.reported} {
		select {
		case <-exited:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// stop drains and shuts the pump down, returning the drain's error when
// ctx ended before it finished (the pump stops all the same, delivering
// what is queued on its own goroutines; a later flush waits for that).
// The channel is never closed, so an enqueue racing with stop is discarded
// rather than panicking. Only the first call drains and reports; later
// calls return nil, so an unfinished drain is reported once.
func (p *eventPump) stop(ctx context.Context) error {
	var err error
	p.stopOnce.Do(func() {
		err = p.flush(ctx)
		p.stopped.Store(true)
		close(p.quit)
	})
	return err
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
// Called from an event listener or from the failure hook handed a dropped
// event, it returns ErrQueryEventsFlushFromPump at once: those run on the
// pump's own goroutines, which the flush would wait for.
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
