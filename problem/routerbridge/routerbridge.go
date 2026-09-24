// Package routerbridge connects the router's error boundary to the error
// pipeline. Install sets the router's error handler so every handler error
// and every recovered panic that reaches the router is reported once and
// rendered once by a contract.ErrorHandler (normally a *problem.Handler),
// with an ErrorContext built from what the router knows about the request.
//
// It is the only package that imports both router and problem, so router
// stays standalone and problem stays free of router types.
package routerbridge

import (
	"net/http"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// Option configures Install.
type Option func(*config)

type config struct {
	resolve func() contract.ErrorHandler
	userID  contract.RequestUserIdentifier
}

// WithHandler sets the function that returns the error handler for a
// failed request. It is called once per failed request, so a handler
// swapped in after Install (for example by a module's Start) is honoured.
// A nil resolver, or one returning nil, falls back to
// router.DefaultErrorHandler.
func WithHandler(resolve func() contract.ErrorHandler) Option {
	return func(c *config) { c.resolve = resolve }
}

// WithUserID sets the facet that names the authenticated user of a failed
// request for ErrorContext.UserID. Nil leaves UserID empty.
func WithUserID(id contract.RequestUserIdentifier) Option {
	return func(c *config) { c.userID = id }
}

// Install sets r's error handler to the bridge. From then on the router
// neither renders nor logs a failed request itself: the resolved error
// handler reports and renders it (see Handle). Like
// router.SetErrorHandler, Install must run before serving begins; a later
// SetErrorHandler call replaces the bridge.
func Install(r *router.VelocityRouterV2, opts ...Option) {
	if r == nil {
		return
	}
	cfg := &config{}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	r.SetErrorHandler(func(c *router.Context, err error, info router.ErrorInfo) {
		var h contract.ErrorHandler
		if cfg.resolve != nil {
			h = cfg.resolve()
		}
		handle(c, err, info, h, cfg.userID)
	})
}

// Handle hands one failed request to h: it builds the ErrorContext from
// info and the request (request, trace and span IDs, method, path, client
// IP, user agent, the recovered flag and both panic stacks) and calls
// h.HandleRequest through c's RenderContext. When info.Committed is true
// the response was already started, so h reports the error and renders
// nothing. A nil h falls back to router.DefaultErrorHandler. Handle leaves
// ErrorContext.UserID empty; Install with WithUserID fills it.
func Handle(c *router.Context, err error, info router.ErrorInfo, h contract.ErrorHandler) {
	handle(c, err, info, h, nil)
}

// handle is Handle with the optional user facet.
func handle(c *router.Context, err error, info router.ErrorInfo, h contract.ErrorHandler, uid contract.RequestUserIdentifier) {
	if c == nil || err == nil {
		return
	}
	if h == nil {
		router.DefaultErrorHandler(c, err, info)
		return
	}
	rc := c.RenderContext()
	if info.Committed {
		rc = committedRenderContext{RenderContext: rc}
	}
	h.HandleRequest(rc, err, errorContext(c, info, uid))
}

// errorContext builds the ErrorContext for a failed request from info and
// c's request.
func errorContext(c *router.Context, info router.ErrorInfo, uid contract.RequestUserIdentifier) *contract.ErrorContext {
	ctx := problem.NewErrorContext()
	ctx.RequestID = info.RequestID
	ctx.TraceID = info.TraceID
	ctx.SpanID = info.SpanID
	ctx.Recovered = info.Recovered
	ctx.PanicStack = info.Stack
	ctx.StackTrace = info.StackTrace
	r := c.Request
	if r == nil {
		return ctx
	}
	ctx.Method = r.Method
	if r.URL != nil {
		// The path only: a query string can carry signatures and tokens
		// that must not reach the logs.
		ctx.URL = r.URL.Path
	}
	ctx.IP = c.IP()
	ctx.UserAgent = r.UserAgent()
	ctx.UserID = userID(uid, r)
	return ctx
}

// userID asks uid for the user of r. A panicking identifier yields "" so a
// failure there never costs the error response.
func userID(uid contract.RequestUserIdentifier, r *http.Request) (id string) {
	if uid == nil {
		return ""
	}
	defer func() {
		if p := recover(); p != nil {
			id = ""
		}
	}()
	return uid.RequestUserID(r)
}

// committedRenderContext is the RenderContext for a response that was
// already started: it reports Written and turns every write into a no-op,
// so an error handler can report the error but never render a second
// response.
type committedRenderContext struct {
	contract.RenderContext
}

// Written always reports true.
func (committedRenderContext) Written() bool { return true }

// WriteHeader does nothing: the status line is already out.
func (committedRenderContext) WriteHeader(int) {}

// Write writes nothing and returns contract.ErrResponseWritten.
func (committedRenderContext) Write([]byte) (int, error) {
	return 0, contract.ErrResponseWritten
}

// SetHeader does nothing: headers can no longer change.
func (committedRenderContext) SetHeader(string, string) {}

// Redirect writes nothing and returns an error matching
// contract.ErrInvalidRedirect.
func (committedRenderContext) Redirect(int, string) error {
	return contract.NewHTTPError(http.StatusInternalServerError).WithCause(contract.ErrInvalidRedirect)
}
