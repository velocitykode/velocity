package problem

import (
	"errors"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// Middleware returns net/http middleware that recovers a panic in next and
// hands it to h as a recovered error (always reported, always a 500). An
// http.ErrAbortHandler panic is passed on so net/http aborts the response
// as documented.
func Middleware(h contract.ErrorHandler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer recoverInto(h, w, r)
			next.ServeHTTP(w, r)
		})
	}
}

// MiddlewareFunc is Middleware for http.HandlerFunc chains.
func MiddlewareFunc(h contract.ErrorHandler) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			defer recoverInto(h, w, r)
			next(w, r)
		}
	}
}

// recoverInto recovers a panic and hands it to h. It must be called
// directly by a deferred statement.
func recoverInto(h contract.ErrorHandler, w http.ResponseWriter, r *http.Request) {
	p := recover()
	if p == nil {
		return
	}
	if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		panic(p)
	}
	ctx := NewErrorContext()
	ctx.Recovered = true
	ctx.PanicStack = string(debug.Stack())
	ctx.StackTrace = contract.CaptureStackTrace(1)
	ctx.Timestamp = time.Now()
	h.HandleRequest(contract.NewRenderContext(w, r), panicerr.FromRecovered(p), ctx)
}

// ErrorHandler returns a function that hands a returned error to h, for
// routers and handlers that surface errors as values.
func ErrorHandler(h contract.ErrorHandler) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		h.HandleRequest(contract.NewRenderContext(w, r), err, nil)
	}
}
