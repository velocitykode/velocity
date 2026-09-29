package interceptors

import (
	"context"
	"errors"
	"runtime/debug"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/eventmeta"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/trace"
)

// CallConfig configures the call lifecycle interceptor CallLifecycle builds.
type CallConfig struct {
	// Logger receives the request line (when RequestLine is set) and the
	// line of a recovered panic the Reporter did not take. Nil means the
	// framework's standalone fallback logger, which writes warnings and
	// errors to standard error.
	Logger contract.Logger

	// RequestLine writes one line per call through Logger: info level,
	// warn for a slow call or a failed one, error for an Internal or
	// Unknown status. Off by default.
	RequestLine bool

	// SkipMethods lists methods that get no request line and no lifecycle
	// events. They are still correlated, recovered and reported.
	SkipMethods map[string]bool

	// SkipHealthChecks treats the gRPC health service's methods as
	// SkipMethods. On by default.
	SkipHealthChecks bool

	// SlowThreshold writes a successful call that ran longer than it at
	// warn level ("gRPC request (slow)") instead of info. Zero disables
	// the rule. The ORM's DB_SLOW_QUERY_THRESHOLD follows the same rule
	// (internal/latency), and both lines write the duration as
	// duration_ms. CallLifecycle defaults it to 5s.
	SlowThreshold time.Duration

	// ExtraFields adds fields to the request line.
	ExtraFields func(ctx context.Context) []interface{}

	// EventDispatcher receives the call's lifecycle events (RequestStarted,
	// RequestFailed, RequestCompleted and their stream counterparts) and
	// PanicRecovered. Nil dispatches none.
	EventDispatcher grpcevents.EventDispatchFunc

	// Reporter, when set, receives the call's one error report: a
	// recovered panic, in place of the Logger's line, or else the error
	// the call ended with when its gRPC code is Internal or Unknown (an
	// error that is neither a gRPC status nor a context error is
	// Unknown), with the method named. Pass the app's error handler
	// (Services.Errors). Reporting never changes what the client gets.
	Reporter contract.Reporter

	// PanicHandler, when set, turns a recovered panic into the error the
	// call ends with, whatever it returns: nil ends the call without an
	// error. Without it the call ends with codes.Internal.
	PanicHandler func(ctx context.Context, p interface{}) error

	// EnableStackTrace captures the stack of a recovered panic for its
	// report, line and event. On by default.
	EnableStackTrace bool

	// events holds EventDispatcher and applies the failure policy to a
	// failed dispatch; CallLifecycle builds it once the options are applied.
	events *eventemit.Emitter
}

// CallOption configures CallLifecycle.
type CallOption func(*CallConfig)

// WithLogger sets the logger the request line and a recovered panic's
// line go to (see CallConfig.Logger for the nil default).
func WithLogger(logger contract.Logger) CallOption {
	return func(c *CallConfig) {
		c.Logger = logger
	}
}

// WithRequestLine turns on the per-call request line (see
// CallConfig.RequestLine).
func WithRequestLine() CallOption {
	return func(c *CallConfig) {
		c.RequestLine = true
	}
}

// WithSkipMethods sets the methods that get no request line and no
// lifecycle events (see CallConfig.SkipMethods).
func WithSkipMethods(methods ...string) CallOption {
	return func(c *CallConfig) {
		c.SkipMethods = make(map[string]bool)
		for _, m := range methods {
			c.SkipMethods[m] = true
		}
	}
}

// WithSkipHealthChecks sets whether health check methods are skipped (see
// CallConfig.SkipHealthChecks).
func WithSkipHealthChecks(skip bool) CallOption {
	return func(c *CallConfig) {
		c.SkipHealthChecks = skip
	}
}

// WithSlowThreshold sets the slow request threshold (see
// CallConfig.SlowThreshold); zero disables it.
func WithSlowThreshold(d time.Duration) CallOption {
	return func(c *CallConfig) {
		c.SlowThreshold = d
	}
}

// WithExtraFields adds fields to the request line.
func WithExtraFields(fn func(ctx context.Context) []interface{}) CallOption {
	return func(c *CallConfig) {
		c.ExtraFields = fn
	}
}

// WithEventDispatcher sets the dispatcher the call's events go to (see
// CallConfig.EventDispatcher).
func WithEventDispatcher(dispatcher grpcevents.EventDispatchFunc) CallOption {
	return func(c *CallConfig) {
		c.EventDispatcher = dispatcher
	}
}

// WithReporter sets the reporter the call's one error report goes to (see
// CallConfig.Reporter).
func WithReporter(reporter contract.Reporter) CallOption {
	return func(c *CallConfig) {
		c.Reporter = reporter
	}
}

// WithPanicHandler sets the handler that turns a recovered panic into the
// call's error (see CallConfig.PanicHandler).
func WithPanicHandler(handler func(ctx context.Context, p interface{}) error) CallOption {
	return func(c *CallConfig) {
		c.PanicHandler = handler
	}
}

// WithStackTrace sets whether a recovered panic's stack is captured.
func WithStackTrace(enabled bool) CallOption {
	return func(c *CallConfig) {
		c.EnableStackTrace = enabled
	}
}

// CallLifecycle returns the interceptor pair that owns a call's observability:
// its correlation, panic recovery, request line, lifecycle events and one
// error report, all from one effective context.
//
// Install the pair once, at both ends of the chain, as a framework-built
// server does; on such a server configure the default one with
// grpc.WithCallOptions instead of adding another. A CallLifecycle nested
// inside a call another one owns passes the call through to that owner,
// so the call still ends once, and the nested one's options have no
// effect:
//
//	calls := interceptors.CallLifecycle(interceptors.WithReporter(reporter))
//	grpc.ChainUnaryInterceptor(calls.Unary, auth.Unary, ..., calls.Unary)
//
// The first occurrence owns the call. It gives the call its span and
// request id (see correlate), dispatches RequestStarted, and when the call
// ends, however it ends, it recovers a panic raised on its goroutine (by
// the handler or by any interceptor after it), then writes the request
// line and dispatches RequestFailed (for an error) and RequestCompleted,
// all under that context. Every started call gets that one terminal
// sequence. User fields (the claims' user_id and team_id, and ExtraFields)
// may come from the context the handler received, which interceptors
// between the two occurrences (Auth, say) extend; correlation never does.
//
// Every later occurrence (the last one, and any nested CallLifecycle)
// publishes the context it passes on and contains a panic below it, for
// the owner's call. The last occurrence contains a panic on the goroutine
// that runs the handler, which may not be the owner's: an interceptor that runs the rest
// of the chain on a goroutine of its own (a timeout, say) cannot pass a
// panic there back to the owner. The call's error report is claimed once:
// whichever layer reports first (a panic, or the owner's Internal or
// Unknown error) is the call's only report. A panic after the owner ended
// the call is reported as late work, with "late" set in its report's
// Extra, and produces no second set of terminal events.
func CallLifecycle(opts ...CallOption) InterceptorPair {
	cfg := &CallConfig{
		SkipHealthChecks: true,
		SlowThreshold:    5 * time.Second,
		EnableStackTrace: true,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	cfg.events = newEventEmitter(cfg.EventDispatcher, cfg.Logger)

	return InterceptorPair{
		Unary:  callsUnary(cfg),
		Stream: callsStream(cfg),
	}
}

// callKey carries, in a call's context, the call its owner built. It does
// not name the owner's config: any CallLifecycle that finds a call in its
// context is inside that call, not a second owner of it.
type callKey struct{}

// call is what the owner of one call shares, through its context, with
// the last occurrence, which runs next to the handler. Every field is set
// before the owner runs the rest of the chain and never written again,
// except two synchronized ones: state, whose bits record that the owner
// ended the call and that a layer claimed the call's one report; and
// handlerCtx, which the last occurrence stores before it calls the handler
// and the owner loads once when it ends the call (a store after that is
// unused).
type call struct {
	cfg    *CallConfig
	ctx    context.Context
	method string
	stream bool
	start  time.Time
	// observed is false for a skipped method: no line, no lifecycle events.
	observed bool
	// state holds callEnded and reportClaimed.
	state atomic.Uint32
	// handlerCtx is the context the handler received (see userContext).
	handlerCtx atomic.Pointer[handlerContext]
}

// handlerContext boxes the context the handler received, for handlerCtx.
type handlerContext struct{ ctx context.Context }

// publish records ctx as the context the handler receives.
func (c *call) publish(ctx context.Context) {
	c.handlerCtx.Store(&handlerContext{ctx: ctx})
}

// userContext returns the context the call's user fields (claims and
// ExtraFields) are read from: the one the handler received, which
// interceptors after the owner may have extended, or the owner's own
// effective context when none was published (an interceptor answered
// without calling the handler, or the chain has no last occurrence). It is
// the only source of user fields; correlation always comes from c.ctx.
func (c *call) userContext() context.Context {
	if h := c.handlerCtx.Load(); h != nil {
		return h.ctx
	}
	return c.ctx
}

// beginCall correlates ctx, builds the call for method and returns it;
// its ctx is the call's effective context, carrying the call.
func beginCall(ctx context.Context, method string, stream bool, cfg *CallConfig) *call {
	c := &call{
		cfg:      cfg,
		method:   method,
		stream:   stream,
		observed: !shouldSkip(method, cfg),
	}
	c.ctx = context.WithValue(correlate(ctx), callKey{}, c)
	c.start = time.Now()
	return c
}

// callOf returns the call ctx is inside, or nil when it is inside none.
func callOf(ctx context.Context) *call {
	c, _ := ctx.Value(callKey{}).(*call)
	return c
}

func callsUnary(cfg *CallConfig) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		if c := callOf(ctx); c != nil {
			defer func() {
				if p := recover(); p != nil {
					resp, err = nil, c.recoverDownstream(ctx, p)
				}
			}()
			c.publish(ctx)
			return handler(ctx, req)
		}
		c := beginCall(ctx, info.FullMethod, false, cfg)
		defer func() {
			p := recover()
			if p != nil {
				resp = nil
			}
			err = c.end(p, err)
		}()
		c.started()
		return handler(c.ctx, req)
	}
}

func callsStream(cfg *CallConfig) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		if c := callOf(ss.Context()); c != nil {
			defer func() {
				if p := recover(); p != nil {
					err = c.recoverDownstream(ss.Context(), p)
				}
			}()
			c.publish(ss.Context())
			return handler(srv, ss)
		}
		c := beginCall(ss.Context(), info.FullMethod, true, cfg)
		defer func() {
			err = c.end(recover(), err)
		}()
		c.started()
		return handler(srv, &tracedServerStream{inner: ss, ctx: c.ctx})
	}
}

// The bits of call.state.
const (
	// callEnded is set once the owner ended the call.
	callEnded uint32 = 1 << iota
	// reportClaimed is set by the layer that reports the call's error.
	reportClaimed
)

// ended reports whether the owner has ended the call.
func (c *call) ended() bool {
	return c.state.Load()&callEnded != 0
}

// claimReport claims the call's one error report, reporting whether this
// caller won it.
func (c *call) claimReport() bool {
	return c.state.Or(reportClaimed)&reportClaimed == 0
}

// recoverDownstream handles panic p, recovered on the goroutine running
// the handler under ctx, and returns the error the handler's layer
// returns. The panic is reported when it wins the call's report claim, or
// when the owner already ended the call: a late panic is always reported
// once, marked late, and never produces terminal events.
func (c *call) recoverDownstream(ctx context.Context, p interface{}) error {
	stack := c.stack()
	if c.claimReport() || c.ended() {
		c.reportPanic(p, stack, c.ended())
	}
	c.panicRecovered(p, stack)
	return c.panicResult(ctx, p)
}

// end ends the call once the chain returned err or panicked with p (nil
// when it did not), and returns the error the call ends with: it handles
// the panic, marks the call ended, then writes the request line,
// dispatches the terminal events and reports an internal error, when the
// panic did not claim the report already.
func (c *call) end(p interface{}, err error) error {
	if p != nil {
		stack := c.stack()
		if c.claimReport() {
			c.reportPanic(p, stack, false)
		}
		c.panicRecovered(p, stack)
		err = c.panicResult(c.ctx, p)
	}
	c.state.Or(callEnded)
	if c.observed {
		user := c.userContext()
		if c.cfg.RequestLine {
			logRequest(c.ctx, user, c.method, c.start, err, c.cfg)
		}
		c.completed(user, err)
	}
	if reportable(c.ctx, err) && c.claimReport() {
		report(c.ctx, c.cfg.Reporter, err, c.method, false, "", false)
	}
	return err
}

// stack returns the calling goroutine's stack when the config captures
// it, else the empty string.
func (c *call) stack() string {
	if !c.cfg.EnableStackTrace {
		return ""
	}
	return string(debug.Stack())
}

// reportable reports whether err, which a call under ctx ended with, is
// an internal error to report: its gRPC code is Internal or Unknown (an
// error that is neither a gRPC status nor a context error is Unknown; see
// statusCodeOf), the codes the request line writes at error level. A call
// its own context ended (the client cancelled, or the deadline passed) is
// not reported.
func reportable(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		return false
	}
	switch statusCodeOf(err) {
	case codes.Internal, codes.Unknown:
		return true
	}
	return false
}

// reportPanic reports panic p, with stack, to the Reporter under the
// call's effective context, or logs it when there is no Reporter (or it
// panicked): one entry. late marks a panic after the call ended.
func (c *call) reportPanic(p interface{}, stack string, late bool) {
	if c.cfg.Reporter != nil && report(c.ctx, c.cfg.Reporter, panicerr.FromRecovered(p), c.method, true, stack, late) {
		return
	}
	fields := []interface{}{"method", c.method, "panic", p}
	if stack != "" {
		fields = append(fields, "stack", stack)
	}
	if late {
		fields = append(fields, "late", true)
	}
	eventemit.WriteLine(c.ctx, c.cfg.Logger, func(l contract.Logger) { l.Error("gRPC panic recovered", fields...) })
}

// panicRecovered dispatches PanicRecovered for p under the call's
// effective context.
func (c *call) panicRecovered(p interface{}, stack string) {
	if !eventsInstalled(c.cfg.events) {
		return
	}
	dispatchEvent(c.ctx, c.cfg.events, &grpcevents.PanicRecovered{
		EventMeta:  eventmeta.Current(c.ctx),
		Method:     c.method,
		Panic:      p,
		StackTrace: stack,
	})
}

// panicResult returns the error a call that panicked with p under ctx
// ends with: whatever the PanicHandler returns (nil included), else
// codes.Internal. A PanicHandler that panics in turn ends the call with
// codes.Internal.
func (c *call) panicResult(ctx context.Context, p interface{}) (err error) {
	internal := status.Errorf(codes.Internal, "internal server error")
	if c.cfg.PanicHandler == nil {
		return internal
	}
	defer func() {
		if recover() != nil {
			err = internal
		}
	}()
	return c.cfg.PanicHandler(ctx, p)
}

// report hands err, a failure of the call to method, to reporter with the
// ErrorContext trace.NewErrorContext builds from the call's ctx (its
// request, trace and span ids), naming the method; recovered marks a
// recovered panic, with its stack, and late one that happened after the
// call ended. It returns whether the reporter returned normally: a
// reporter that panics is contained, since reporting must never fail the
// call or crash the server.
func report(ctx context.Context, reporter contract.Reporter, err error, method string, recovered bool, stack string, late bool) (reported bool) {
	if reporter == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			reported = false
		}
	}()
	ec := trace.NewErrorContext(ctx)
	ec.Recovered, ec.PanicStack = recovered, stack
	ec.Extra["method"] = method
	if late {
		ec.Extra["late"] = true
	}
	reporter.Report(err, ec)
	return true
}

// started dispatches the call's started event.
func (c *call) started() {
	if !c.observed || !eventsInstalled(c.cfg.events) {
		return
	}
	var md map[string][]string
	if inMD, ok := metadata.FromIncomingContext(c.ctx); ok {
		md = redactMetadata(inMD)
	}
	meta := eventmeta.Current(c.ctx)
	meta.At = c.start
	protocol := detectProtocol(c.ctx)
	var event interface{} = &grpcevents.RequestStarted{EventMeta: meta, Method: c.method, Protocol: protocol, Metadata: md}
	if c.stream {
		event = &grpcevents.StreamStarted{EventMeta: meta, Method: c.method, Protocol: protocol, Metadata: md}
	}
	dispatchEvent(c.ctx, c.cfg.events, event)
}

// completed dispatches the end of the call: the failed event when it
// ended with err, then the completed event, the terminal event of every
// call. The claims are read from user (see userContext).
func (c *call) completed(user context.Context, err error) {
	if !eventsInstalled(c.cfg.events) {
		return
	}
	meta := eventmeta.Current(c.ctx)
	duration := meta.At.Sub(c.start)
	protocol := detectProtocol(c.ctx)
	code := statusCodeOf(err)
	var userID, teamID uint
	if claims := ClaimsFromContext(user); claims != nil {
		userID, teamID = claims.GetUserID(), claims.GetTeamID()
	}
	if err != nil {
		var failed interface{} = &grpcevents.RequestFailed{EventMeta: meta, Method: c.method, Protocol: protocol,
			Duration: duration, StatusCode: code, Err: err, UserID: userID, TeamID: teamID}
		if c.stream {
			failed = &grpcevents.StreamFailed{EventMeta: meta, Method: c.method, Protocol: protocol,
				Duration: duration, StatusCode: code, Err: err, UserID: userID, TeamID: teamID}
		}
		dispatchEvent(c.ctx, c.cfg.events, failed)
	}
	var completed interface{} = &grpcevents.RequestCompleted{EventMeta: meta, Method: c.method, Protocol: protocol,
		Duration: duration, StatusCode: code, UserID: userID, TeamID: teamID}
	if c.stream {
		completed = &grpcevents.StreamCompleted{EventMeta: meta, Method: c.method, Protocol: protocol,
			Duration: duration, StatusCode: code, UserID: userID, TeamID: teamID}
	}
	dispatchEvent(c.ctx, c.cfg.events, completed)
}
