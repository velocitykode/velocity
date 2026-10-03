// Package teardown runs the steps of a component's shutdown contained: a
// step that panics becomes that step's error, so one bad child (a module,
// a store, a channel, a disk) cannot abort the shutdown of the ones after
// it. The caller runs every step and joins the errors.
//
// Close is the one closer contract: a value closes through its
// Shutdown(ctx) error, its Close() error, or, for a func, by being
// called. Drain stops a component whose stop may return at its ctx while
// its work goes on, and waits for that work. Children is a manager's
// lifecycle of the children it holds (its stores, channels, disks,
// connections), one run at a time, on internal/drain.
//
// It imports the standard library, contract and the internal leaves
// drain, errchain, fallbacklog, nilval and panicerr only.
package teardown

import (
	"context"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// Step runs one teardown step and returns its error, or the panic it
// raised as a *panicerr.Error holding the raw recovered value (formatted
// only when the error is read). A step that calls runtime.Goexit is not a
// panic: Goexit goes on to end the calling goroutine.
func Step(step func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = panicerr.FromRecovered(r)
		}
	}()
	return step()
}

// closer returns how v closes, and whether it takes the ctx: its
// Shutdown(ctx) error (contract.ShutdownAware, log.Shutdowner), else its
// Close() error (an ORM driver, an io.Closer), else v itself when it is a
// func(context.Context) error. It returns nil when v has none of them.
// The method is reached inside the returned func, never here: on a nil
// pointer with a value receiver, reaching it panics, and that panic
// belongs to the contained call.
func closer(v any) (close func(context.Context) error, takesCtx bool) {
	switch c := v.(type) {
	case interface{ Shutdown(context.Context) error }:
		return func(ctx context.Context) error { return c.Shutdown(ctx) }, true
	case interface{ Close() error }:
		return func(context.Context) error { return c.Close() }, false
	case func(context.Context) error:
		return c, true
	}
	return nil, false
}

// Close closes v as one Step through the closer contract: its
// Shutdown(ctx) error, else its Close() error, else v itself when it is a
// func(context.Context) error. It returns nil when v has none of them. A
// manager uses it for a child it built but never published and for a
// child that left its registry.
func Close(ctx context.Context, v any) error {
	close, _ := closer(v)
	if close == nil {
		return nil
	}
	return Step(func() error { return close(ctx) })
}

// Drain runs stop with ctx, contained, and returns its result. A stop
// that drains admitted work returns ctx's error at ctx while the work
// goes on: when stop failed and ctx is done, Drain calls stop again with
// ctx detached from its cancellation and returns that result, the stop's
// retained one (contract.ShutdownAware: a repeated stop returns the
// result of the first), so the caller goes on only once the work
// finished. A panic in stop is its result and is not repeated. A nil ctx
// is context.Background.
func Drain(ctx context.Context, stop func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	err := Step(func() error { return stop(ctx) })
	if err != nil && ctx.Err() != nil && panicerr.AsTyped(err) == nil {
		return Step(func() error { return stop(context.WithoutCancel(ctx)) })
	}
	return err
}

// closeChild closes a child a Shutdown detached: through Drain when its
// closer takes the ctx, so a child that returns at ctx is waited for,
// else once.
func closeChild(ctx context.Context, v any) error {
	close, takesCtx := closer(v)
	switch {
	case close == nil:
		return nil
	case takesCtx:
		return Drain(ctx, close)
	}
	return Step(func() error { return close(ctx) })
}

// Warn writes, once, the close error of a child Children.Retire closed
// for a call that succeeded (a replace, a removal, a clear, a discarded
// duplicate): the call returns no error for it, so it is a warning,
// through l, or the fallback logger when l is nil or panics. manager
// names the package ("mail"), name the child. A nil err writes nothing.
func Warn(l contract.Logger, manager, name string, err error) {
	if err == nil {
		return
	}
	fallbacklog.Write(l, func(l contract.Logger) {
		l.Warn("velocity/"+manager+": a displaced child failed to shut down", "name", name, "error", err)
	})
}
