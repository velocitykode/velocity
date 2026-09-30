package m

import (
	"context"

	"example.com/m/internal/eventemit"
)

// sharing shares the app's Failures: its Fail calls are fine.
type sharing struct{ events eventemit.Emitter }

func (s *sharing) ShareEventFailures(f *eventemit.Failures) { s.events.Share(f) }

func (s *sharing) drop(ctx context.Context, err error) {
	s.events.Fail(ctx, err, nil)
	_ = s.events.FailLater(ctx, err, nil)
}

// own never shares: a failure it originates misses the app counter.
type own struct {
	events eventemit.Emitter
	local  func(context.Context, any) error
}

func (o *own) drop(ctx context.Context, err error) {
	o.events.Fail(ctx, err, nil)          // want emitter
	_ = o.events.FailLater(ctx, err, nil) // want emitter
	(&o.events).Fail(ctx, err, nil)       // want emitter
	o.events.Share(nil)                   // a nil Failures shares nothing
	o.events.SetShared(o.local, nil, nil) // nor does SetShared with nil
}

// ownDispatch records the result of its own dispatcher: in an app that is
// the app's dispatch function, which recorded it.
func (o *own) ownDispatch(ctx context.Context, ev any) {
	dispatch := o.events.Dispatcher()
	if err := eventemit.DispatchContained(ctx, dispatch, ev); err != nil {
		o.events.Fail(ctx, err, ev)
	}
	o.events.Fail(ctx, eventemit.DispatchContained(ctx, o.events.Dispatcher(), ev), ev)
}

// localDispatch records the result of another dispatcher: reported.
func (o *own) localDispatch(ctx context.Context, ev any) {
	if err := eventemit.DispatchContained(ctx, o.local, ev); err != nil {
		o.events.Fail(ctx, err, ev) // want emitter
	}
	var other own
	if err := eventemit.DispatchContained(ctx, other.events.Dispatcher(), ev); err != nil {
		o.events.Fail(ctx, err, ev) // want emitter
	}
}

// reassigned is a dispatcher variable assigned twice: not provably own.
func (o *own) reassigned(ctx context.Context, ev any) {
	dispatch := o.events.Dispatcher()
	dispatch = o.local
	if err := eventemit.DispatchContained(ctx, dispatch, ev); err != nil {
		o.events.Fail(ctx, err, ev) // want emitter
	}
}

// global is a package-level emitter handed an app's Failures in one step.
var global eventemit.Emitter

func SetSharedGlobal(fn func(context.Context, any) error, f *eventemit.Failures) {
	global.SetShared(fn, f, nil)
}

func globalDrop(ctx context.Context, err error) { global.Fail(ctx, err, nil) }

// lonely is a package-level emitter nothing shares.
var lonely eventemit.Emitter

func lonelyDrop(ctx context.Context, err error) { lonely.Fail(ctx, err, nil) } // want emitter
