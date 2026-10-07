package router

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// commitListener is a pre-commit listener (see Context.BeforeCommit).
type commitListener = func(status int, w http.ResponseWriter)

// The dispatch states of a responseWriter's commit listeners.
const (
	// commitIdle: listeners may register; none has run.
	commitIdle uint8 = iota
	// commitDispatching: listeners are running. The writer refuses to
	// commit, to hand the connection over and to register (see dispatch).
	commitDispatching
	// commitResumed: a listener panicked out of a dispatch and listeners
	// that had not run are still held. They run with the next commit;
	// nothing registers any more.
	commitResumed
	// commitDone: every listener ran or panicked, or a successful Hijack
	// dropped them. Nothing registers and nothing runs again.
	commitDone
)

// errCommitDispatch is what Write and Hijack return when a commit listener
// calls them: a listener changes headers only.
var errCommitDispatch = errors.New("velocity/router: the response is being committed: a commit listener changes headers only")

// responseWriter wraps http.ResponseWriter to capture response metrics. It
// is the router's own writer and the commit owner of the request: it holds
// the pre-commit listeners registered through Context.BeforeCommit and runs
// them just before the response headers are committed.
//
// One request, one writer that records its commitment; every other layer
// asks it. A responseWriter over a writer that reports its own commitment
// (contract.CommitReporter: another responseWriter, when a handler mounts
// a router or Wrap inside a request, or an application writer that
// reports) keeps no record of its own: Committed, and every decision this
// type takes on it, answers from that writer (see bind and Committed). So
// a write the writer underneath did not take, as a responseWriter refuses
// one made while its listeners run, is a commitment nowhere in the stack.
// It records for itself only over a writer that cannot say.
//
// The wrapper is used from the request's goroutine only, so the listener
// registry needs no lock, and no listener ever runs under one (a listener
// may block or call back into the writer).
type responseWriter struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
	// wroteHeader is this writer's own record of the commitment. It is
	// the truth only while below is nil; read Committed, never this field.
	wroteHeader bool
	// below is the writer underneath when that writer reports its own
	// commitment, nil otherwise and after a successful Hijack (see bind).
	below contract.CommitReporter

	// commitState is the dispatch state of the listeners. It is an explicit
	// state, not a sync.Once: a listener that writes through the wrapper
	// finds it claimed and is turned away, where a Once would wait on
	// itself.
	commitState uint8
	// listener is the first registered listener, held inline so a request
	// with one listener (the session middleware's save) allocates nothing
	// for the registry. Non-nil exactly while listeners are pending or
	// running.
	listener commitListener
	// more holds the listeners registered after the first, oldest first. Its
	// backing array is kept across pool cycles and holds no listener past
	// a dispatch or a release.
	more []commitListener
	// finalized says the creator of this writer finalizes it: once the
	// request is over it runs the listeners nothing ran (the router for
	// its pooled writers, Wrap for its own). False for a writer bound on
	// demand to a Context nobody finalizes. It belongs to the writer, so
	// it holds for every Context that borrows this owner.
	finalized bool
	// attempts counts the listener invocation attempts made on this
	// writer: it goes up by one each time dispatch takes a listener out
	// to call it, and never down. It is the one measure of progress the
	// router's panic path uses (see VelocityRouterV2.onPanic).
	attempts uint32
	// framing is the destination's header map, taken by the first
	// dispatch of the response and dropped when the writer is released
	// (see dispatch and closeDispatch).
	framing http.Header
	// dispatchBroke is set while a dispatch runs and cleared when it
	// ends cleanly, so the deferred closeDispatch knows a listener
	// panicked out of it.
	dispatchBroke bool
}

// responseWriterPool recycles responseWriter wrappers across requests so
// ServeHTTP does not heap-allocate one per request. Acquire resets every
// field (the listener registry included) so no state leaks between
// requests.
var responseWriterPool = sync.Pool{
	New: func() any { return &responseWriter{} },
}

// acquireResponseWriter pulls a responseWriter from the pool and resets
// it to wrap w with a clean state: default 200 status, zero bytes, no
// header written, no listener.
func acquireResponseWriter(w http.ResponseWriter) *responseWriter {
	rw := responseWriterPool.Get().(*responseWriter)
	rw.bind(w)
	rw.status = http.StatusOK // Default status
	rw.bytesWritten = 0
	rw.wroteHeader = false
	rw.dropListeners()
	rw.commitState = commitIdle
	rw.dispatchBroke = false
	rw.framing = nil
	rw.attempts = 0
	// Only the router acquires pooled writers, and it finalizes them.
	rw.finalized = true
	return rw
}

// releaseResponseWriter returns rw to the pool after the response
// completes. A pooled wrapper holds no listener and no closure: it must
// not pin the previous request's ResponseWriter or anything a listener
// captured.
func releaseResponseWriter(rw *responseWriter) {
	rw.ResponseWriter = nil
	rw.below = nil
	rw.framing = nil
	rw.dropListeners()
	responseWriterPool.Put(rw)
}

// bind points rw at the writer it stands on, and is the one place that
// decides who records the commitment: w, when it reports its own, and rw
// otherwise. Every responseWriter is bound through it.
func (rw *responseWriter) bind(w http.ResponseWriter) {
	rw.ResponseWriter = w
	rw.below, _ = w.(contract.CommitReporter)
}

// dropListeners forgets every registered listener, the ones behind the
// overflow slice's length included, and keeps the slice's backing array.
func (rw *responseWriter) dropListeners() {
	rw.listener = nil
	if len(rw.more) > 0 {
		clear(rw.more)
		rw.more = rw.more[:0]
	}
}

// addListener registers fn behind the listeners already registered. It
// refuses a nil fn and any registration once the response is committed or
// the listeners are running or done: such a listener could never run, or
// would run after the headers left.
func (rw *responseWriter) addListener(fn commitListener) bool {
	if fn == nil || rw.commitState != commitIdle || rw.Committed() {
		return false
	}
	if rw.listener == nil {
		rw.listener = fn
		return true
	}
	rw.more = append(rw.more, fn)
	return true
}

// dispatch runs the pending listeners with status, the status of the
// commit being attempted, and rw. The caller checked that a listener is
// pending. This function is the one place that decides their order: the
// last registered runs first, the way middleware unwinds, so a listener
// registered by an outer middleware sees what the inner ones did to the
// response.
//
// Every listener is attempted once per response, ever: it is taken out of
// the registry just before it is called, so whatever it does (return,
// panic, call back in) it cannot run again, and what the registry holds is
// always exactly the listeners not yet attempted. All of them run exactly
// once when the response commits an HTTP status. The state is claimed
// before the first one runs. While it is claimed the writer refuses to
// commit (WriteHeader and Flush do nothing, Write and Hijack return
// errCommitDispatch) and to register, so a listener cannot replace the
// status the others were given, commit ahead of the listeners after it, or
// deadlock by calling back in. No lock is held: a listener that blocks
// holds its own request only.
//
// A panic in a listener is not recovered here: it unwinds to the router's
// boundary, which answers a 500 and reports it once (see
// VelocityRouterV2.onPanic). The listeners not yet attempted stay in the
// registry and run with that answer's commit, so each of them still sees
// the status the client gets; the ones attempted before saw the earlier
// status. The panic leaves marked as a listener's (see closeDispatch).
func (rw *responseWriter) dispatch(status int) {
	// The destination's header map is taken once per response, before
	// anything is claimed, and kept: closing a broken dispatch, and the
	// dispatch that resumes after it, then need no call into the
	// destination (see closeDispatch). A destination whose Header panics
	// does so here, with every listener still pending and the state
	// untouched: an ordinary panic of the request.
	if rw.framing == nil {
		rw.framing = rw.ResponseWriter.Header()
	}
	rw.commitState = commitDispatching
	rw.dispatchBroke = true
	defer rw.closeDispatch()
	for n := len(rw.more); n > 0; n = len(rw.more) {
		fn := rw.more[n-1]
		rw.more[n-1] = nil
		rw.more = rw.more[:n-1]
		rw.attempts++
		fn(status, rw)
	}
	// The first registered runs last. The inline slot stays set until
	// then: non-nil is what says listeners are pending.
	fn := rw.listener
	rw.listener = nil
	rw.attempts++
	fn(status, rw)
	rw.dispatchBroke = false
}

// closeDispatch is the one exit of a dispatch, deferred directly by it, so
// it is the frame that recovers a listener's panic. A clean dispatch
// attempted every listener and is done.
//
// A broken one, in this order. The dispatch state is restored first, so
// the writer is never left refusing writes: resumed when listeners not yet
// attempted remain, which wait for the fallback's commit with registration
// still refused; done otherwise. Then the framing is dropped: the response
// is left for a fallback to answer, and the Content-Length the listeners
// that ran, or the handler, set is for a body that fallback will not send
// (net/http would cut the fallback's own body short or refuse it). The
// entry is deleted from the header map dispatch took before the first
// listener ran, so nothing here calls into the destination and nothing
// here can fail. Last,
// the listener's panic goes on, as a *panicerr.Listener carrying its value
// and naming this writer as the owner whose listener panicked, which is
// how the frames it unwinds through, and the router's boundary at the end,
// know a listener raised it (see panicerr.Listener). An
// http.ErrAbortHandler goes on as itself.
func (rw *responseWriter) closeDispatch() {
	if !rw.dispatchBroke {
		rw.commitState = commitDone
		return
	}
	p := recover() //recover-ok: the origin: marks a listener panic and re-panics it, aborts go on as themselves
	if rw.listener != nil {
		rw.commitState = commitResumed
	} else {
		rw.commitState = commitDone
	}
	delete(rw.framing, "Content-Length")
	if p == nil {
		return
	}
	if isAbortPanic(p) {
		panic(p)
	}
	panic(panicerr.NewListener(p, rw))
}

// endDispatch closes the registry: nothing runs or registers again, and
// the writer holds no listener.
func (rw *responseWriter) endDispatch() {
	rw.commitState = commitDone
	rw.dropListeners()
}

// pending returns how many listeners the registry holds, none of them
// attempted yet.
func (rw *responseWriter) pending() int {
	n := len(rw.more)
	if rw.listener != nil {
		n++
	}
	return n
}

// WriteHeader writes statusCode through and records it as the response
// status. An informational status (1xx other than 101 Switching
// Protocols, such as 103 Early Hints) goes through without committing the
// response: the commit listeners do not run, the recorded status stays,
// and the handler's final WriteHeader still lands.
//
// A status outside 100-999 is refused here, by a panic with net/http's own
// message for it, before the destination or any listener sees it: net/http
// panics on such a status, and the writer does the same itself so that the
// outcome does not rest on what the writer underneath does with one (a
// lenient one would commit headers the listeners never saw). It is an
// ordinary panic of the request, not a listener's: the response is
// uncommitted and every listener still pending, so the boundary's 500 runs
// them.
//
// The commit listeners run first, with statusCode: this is the last moment
// a header change still reaches the client. Called from inside a listener
// it does nothing.
//
// The status and the commitment are recorded only once the underlying
// writer accepted the status: a destination that panics instead leaves the
// response uncommitted, so the router's boundary still answers it with a
// 500, and so does one that reports its own commitment and reports none
// after the call (it refused the write).
func (rw *responseWriter) WriteHeader(statusCode int) {
	if rw.commitState == commitDispatching || rw.Committed() {
		return
	}
	if statusCode < 100 || statusCode > 999 {
		panic(fmt.Sprintf("invalid WriteHeader code %v", statusCode))
	}
	if statusCode <= 199 && statusCode != http.StatusSwitchingProtocols {
		rw.ResponseWriter.WriteHeader(statusCode)
		return
	}
	if rw.listener != nil {
		rw.dispatch(statusCode)
	}
	rw.ResponseWriter.WriteHeader(statusCode)
	rw.wroteHeader = true
	if rw.Committed() {
		rw.status = statusCode
	}
}

// Write captures the bytes written. The first Write of an uncommitted
// response commits it with 200, running the commit listeners first. Called
// from inside a listener it writes nothing and returns an error.
func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.Committed() {
		// Listeners only ever run on an uncommitted response, so the
		// committed path never looks at the dispatch state.
		if rw.commitState == commitDispatching {
			return 0, errCommitDispatch
		}
		rw.WriteHeader(http.StatusOK)
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytesWritten += int64(n)
	return n, err
}

// Status returns the captured status code
func (rw *responseWriter) Status() int {
	return rw.status
}

// BytesWritten returns the total bytes written
func (rw *responseWriter) BytesWritten() int64 {
	return rw.bytesWritten
}

// Committed reports whether the response is committed: a final status
// line (an informational 1xx other than 101 does not count), a body byte,
// a flush or a successful Hijack has reached the underlying writer, after
// which no second response may be written. It implements
// contract.CommitReporter, so a contract.NewRenderContext over this writer
// sees the commitment.
//
// It is the one read of the commitment, for this type's own decisions as
// well. Over a writer that reports its own commitment it is that writer's
// answer, which covers a write that writer did not take and a commitment
// made past this one; this writer's own record answers only over a writer
// that cannot say.
func (rw *responseWriter) Committed() bool {
	return contract.IsCommitted(rw.below, rw.wroteHeader || rw.bytesWritten > 0)
}

// The router's writer reports its own commitment.
var _ contract.CommitReporter = (*responseWriter)(nil)

// Unwrap returns the underlying ResponseWriter (for http.ResponseController)
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// Hijack implements http.Hijacker for WebSocket support.
//
// A raw takeover has no HTTP status, so the commit listeners do not run:
// there is no status to give them, and the headers they would set are not
// sent by a connection the caller now writes itself. They are dropped only
// once the takeover succeeded; a refused or unsupported Hijack leaves them
// armed for the response that follows. Called from inside a listener it
// returns an error and leaves the connection alone.
//
// A successful Hijack commits the response: the connection belongs to
// the caller, so a later WriteHeader is a no-op and an error returned
// after the upgrade is answered with nothing written.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if rw.commitState == commitDispatching {
		return nil, nil, errCommitDispatch
	}
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		conn, brw, err := h.Hijack()
		if err == nil {
			// The connection is the caller's: there is no response
			// left for the writer underneath to report on, so this
			// writer's own record answers from here on.
			rw.below = nil
			rw.wroteHeader = true
			rw.endDispatch()
		}
		return conn, brw, err
	}
	return nil, nil, http.ErrNotSupported
}

// Flush implements http.Flusher for streaming support.
// The first Flush of an uncommitted response runs the commit listeners
// with 200 before delegating, because Go's http.ResponseWriter commits
// response headers on the first Flush call (chunkWriter writes the status
// line + headers before the buffered body bytes hit the wire). A handler
// that calls c.Response.(http.Flusher).Flush() before any explicit
// WriteHeader / Write would otherwise commit past the listeners. Called
// from inside a listener it does nothing; over a writer that cannot flush
// it does nothing either, and commits nothing.
//
// Marks wroteHeader, once the inner Flush returned, so a subsequent Write
// does not trigger a second implicit WriteHeader on the inner
// ResponseWriter (which would log "superfluous response.WriteHeader
// call"). The wrapper's status field stays at its default http.StatusOK
// because that is what Go's inner flush emits implicitly.
//
// Repeated Flush calls during a long stream (SSE keepalive ticks) find the
// response committed and only flush.
func (rw *responseWriter) Flush() {
	f, ok := rw.ResponseWriter.(http.Flusher)
	if !ok {
		return
	}
	if !rw.Committed() {
		if rw.commitState == commitDispatching {
			return
		}
		if rw.listener != nil {
			rw.dispatch(http.StatusOK)
		}
	}
	f.Flush()
	rw.wroteHeader = true
}

// Push implements http.Pusher for HTTP/2 server push.
//
// Per the http.Pusher contract, Push initiates a separate server-push
// stream (a synthesized GET on the target) carried on its own HTTP/2
// stream; it does NOT commit headers on the main response. The commit
// listeners do not run here: a save-at-end Set-Cookie still needs to wait
// for the main response's first WriteHeader/Write so the cookie lands on
// the response the client requested, not the pushed asset.
func (rw *responseWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := rw.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}
