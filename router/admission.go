package router

import (
	"context"
	"net/http"

	"github.com/velocitykode/velocity/contract"
)

// The router's work in flight is drained through internal/drain: one
// Owner per router, whose request run admits every ServeHTTP from its
// start to its return, and one run per async event pool, whose workers
// are its admitted units. The request run is made with the router and
// ends at its Shutdown: a router does not serve again after it.
//
// A request is not recorded goroutine by goroutine (that would cost every
// request a goroutine lookup): the owner names ServeHTTP and the Timeout
// handler goroutine (runTimed) as running its work, so a stop called from
// a handler is recognised at stop time, by its stack. A handler of one
// router stopping another router is refused the same way.

// initAdmission makes the router's request run; NewV2 calls it.
func (r *VelocityRouterV2) initAdmission() {
	r.own.NestedInside((*VelocityRouterV2).ServeHTTP, runTimed)
	r.requests = r.own.NewRun()
}

// refuse answers a request that arrived once the router's stop began: 503
// with Retry-After and Connection: close, through the router's error
// boundary, with nothing of the application run. The refusal's cause is
// contract.ErrServerShuttingDown, which neither boundary reports.
func (r *VelocityRouterV2) refuse(w http.ResponseWriter, req *http.Request) {
	rw := acquireResponseWriter(w)
	defer releaseResponseWriter(rw)
	req = r.servedRequest(req)
	ctx := r.ctxPool.Get().(*Context)
	ctx.Response = rw
	ctx.commit = rw
	ctx.Request = req
	ctx.applyWiring(r.currentWiring())
	ctx.requests = nil // refused: it holds no unit of the run
	defer func() {
		var abort any
		if recovered := recover(); recovered != nil { //recover-ok: a router boundary: aborts go on, every other value goes to onPanic
			if isAbortPanic(recovered) {
				abort = recovered
			} else {
				abort = r.onPanic(ctx, rw, req, requestMeta{}, recovered)
			}
		}
		ctx.reset()
		r.ctxPool.Put(ctx)
		if abort != nil {
			panic(abort)
		}
	}()
	r.handleError(ctx, rw, shutdownRefusal{serverShutdownError(contract.ErrServerShuttingDown)}, ErrorInfo{})
}

// shutdownRefusal is the error a refused request is answered with: the
// HTTPError of a request the server cut off while shutting down (503,
// Retry-After: 1, Connection: close, caused by
// contract.ErrServerShuttingDown), wrapped so that it is not reported:
// the refusal is an outcome of the shutdown, not a failure, and an
// HTTPError at 503 would ask to be reported. An error handler matching
// *contract.HTTPError still finds the 503.
type shutdownRefusal struct{ *contract.HTTPError }

// ShouldReport reports false: a refusal is never reported.
func (r shutdownRefusal) ShouldReport() bool { return false }

// Unwrap returns the HTTPError.
func (r shutdownRefusal) Unwrap() error { return r.HTTPError }

// Shutdown stops the router: it refuses every request that arrives from
// now on (503, see refuse), waits for the requests it admitted to return,
// then stops every async event pool it started, a replaced one included,
// once each has delivered the events it buffered.
//
// It waits within ctx: at ctx it returns ctx's error and the stop goes on
// without a bound; the router forces nothing. A request served through the
// App's server is cancelled at the App's deadline; one served through
// another host ends when that host closes it. Every later Shutdown
// returns the first one's result once the stop finished. Called from a
// request handler or an event listener of the router, which the stop
// waits for, it is refused with an error wrapping
// contract.ErrStopFromOwnWork and changes nothing. A done ctx begins the
// stop and returns at once.
func (r *VelocityRouterV2) Shutdown(ctx context.Context) error {
	return r.own.Stop(ctx, r.requests, r.stopWork, nil)
}

// stopWork is the router's stop: the admitted requests, then the pools.
func (r *VelocityRouterV2) stopWork() error {
	<-r.requests.Idle()
	for _, p := range r.startedPools() {
		r.own.Signal(p.run, p.work)
		<-p.run.Finished()
	}
	return nil
}

// OwnsCaller reports whether the calling goroutine runs the router's own
// work: a request handler (see Shutdown) or an async event pool's
// listener. App.Shutdown asks it because its teardown waits for that
// work, so a Shutdown called from it is refused instead. A goroutine a
// handler starts on its own is not recognised.
func (r *VelocityRouterV2) OwnsCaller() bool {
	return r.own.Nested()
}

// requestsStopping reports whether the router's stop began.
func (r *VelocityRouterV2) requestsStopping() bool {
	return r.requests.Stopping()
}

var _ contract.ShutdownAware = (*VelocityRouterV2)(nil)
