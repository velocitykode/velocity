package interceptors

import (
	"context"
	"errors"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/eventmeta"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/trace"
)

// RecoveryConfig configures the recovery interceptor
type RecoveryConfig struct {
	// Logger receives one error line per recovered panic the Reporter did
	// not take. Nil means the framework's standalone fallback logger, which
	// writes it to standard error.
	Logger contract.Logger

	// EnableStackTrace enables logging of stack traces on panic
	EnableStackTrace bool

	// PanicHandler is an optional custom handler for panics.
	// If set, it's called before the standard error response is returned.
	// Return an error to override the default internal error response.
	PanicHandler func(ctx context.Context, p interface{}) error

	// EventDispatcher routes grpcevents.PanicRecovered to a listener.
	EventDispatcher grpcevents.EventDispatchFunc

	// Reporter, when set, receives every recovered panic, in place of the
	// Logger's line, and every internal error a handler returns (its gRPC
	// code is Internal or Unknown; an error that is neither a gRPC status
	// nor a context error is Unknown), each once, with the method named.
	// Pass the app's error handler (Services.Errors). Reporting never
	// changes what the client gets.
	Reporter contract.Reporter

	// events holds EventDispatcher and applies the failure policy to a
	// failed dispatch; Recovery builds it once the options are applied.
	events *eventemit.Emitter
}

// RecoveryOption configures recovery behavior
type RecoveryOption func(*RecoveryConfig)

// WithRecoveryLogger sets the logger recovered panics go to (see
// RecoveryConfig.Logger for the nil default).
func WithRecoveryLogger(logger contract.Logger) RecoveryOption {
	return func(c *RecoveryConfig) {
		c.Logger = logger
	}
}

// WithStackTrace enables stack trace logging
func WithStackTrace(enabled bool) RecoveryOption {
	return func(c *RecoveryConfig) {
		c.EnableStackTrace = enabled
	}
}

// WithPanicHandler sets a custom panic handler
func WithPanicHandler(handler func(ctx context.Context, p interface{}) error) RecoveryOption {
	return func(c *RecoveryConfig) {
		c.PanicHandler = handler
	}
}

// WithRecoveryEventDispatcher routes grpcevents.PanicRecovered to a dispatcher
// when the recovery interceptor catches a panic.
func WithRecoveryEventDispatcher(dispatcher grpcevents.EventDispatchFunc) RecoveryOption {
	return func(c *RecoveryConfig) {
		c.EventDispatcher = dispatcher
	}
}

// WithRecoveryReporter sets the reporter recovered panics and internal
// handler errors are reported to (see RecoveryConfig.Reporter).
func WithRecoveryReporter(reporter contract.Reporter) RecoveryOption {
	return func(c *RecoveryConfig) {
		c.Reporter = reporter
	}
}

// Recovery creates a recovery interceptor pair that recovers from panics.
// It reports the panic (with a Reporter) or logs it, and returns an
// internal error to the client. With a Reporter it also reports the
// internal errors handlers return.
//
// The same pair may run at both ends of a chain, as a framework-built
// server runs its default recovery: the last one recovers the handler's
// panic, so every interceptor between the two (Logging's lifecycle events
// and line included) sees the call end with the error the panic became,
// and the first one recovers a panic in those interceptors. A panic is
// reported once, and the error it became is not reported again as an
// internal error.
func Recovery(opts ...RecoveryOption) InterceptorPair {
	cfg := &RecoveryConfig{
		EnableStackTrace: true,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	cfg.events = newEventEmitter(cfg.EventDispatcher, cfg.Logger)

	return InterceptorPair{
		Unary:  recoveryUnary(cfg),
		Stream: recoveryStream(cfg),
	}
}

// RecoveryInterceptor creates a unary recovery interceptor.
// This is a convenience function for when you only need the unary interceptor.
func RecoveryInterceptor(opts ...RecoveryOption) grpc.UnaryServerInterceptor {
	return Recovery(opts...).Unary
}

// StreamRecoveryInterceptor creates a stream recovery interceptor.
// This is a convenience function for when you only need the stream interceptor.
func StreamRecoveryInterceptor(opts ...RecoveryOption) grpc.StreamServerInterceptor {
	return Recovery(opts...).Stream
}

// recoveryCallKey carries, in a call's context, the state the recovery
// interceptor built from cfg keeps for the call (see recoveryCall).
type recoveryCallKey struct{ cfg *RecoveryConfig }

// recoveryCall is the state one recovery interceptor keeps for a call it
// runs at both ends of the chain. The first layer (the one that finds no
// state in the context) installs it; the last layer, finding it, recovers
// the handler's panic there and records that it did, so the first layer
// does not report the error the panic became as an internal error as
// well. The layers run in turn on the goroutine that serves the call.
type recoveryCall struct{ recovered bool }

// callOf returns the state the first layer of cfg's interceptor installed
// in ctx, or nil when ctx holds none.
func callOf(ctx context.Context, cfg *RecoveryConfig) *recoveryCall {
	call, _ := ctx.Value(recoveryCallKey{cfg: cfg}).(*recoveryCall)
	return call
}

func recoveryUnary(cfg *RecoveryConfig) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		if call := callOf(ctx, cfg); call != nil {
			// The last layer: the handler's panic ends the call here, as
			// its error, inside every interceptor between the layers.
			defer func() {
				if r := recover(); r != nil {
					call.recovered = true
					err = handlePanic(ctx, r, info.FullMethod, cfg)
				}
			}()
			return handler(ctx, req)
		}
		call := &recoveryCall{}
		ctx = context.WithValue(ctx, recoveryCallKey{cfg: cfg}, call)
		defer func() {
			if r := recover(); r != nil {
				err = handlePanic(ctx, r, info.FullMethod, cfg)
			}
		}()
		resp, err = handler(ctx, req)
		if !call.recovered {
			reportInternalError(ctx, err, info.FullMethod, cfg)
		}
		return resp, err
	}
}

func recoveryStream(cfg *RecoveryConfig) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		if call := callOf(ss.Context(), cfg); call != nil {
			// The last layer (see recoveryUnary).
			defer func() {
				if r := recover(); r != nil {
					call.recovered = true
					err = handlePanic(ss.Context(), r, info.FullMethod, cfg)
				}
			}()
			return handler(srv, ss)
		}
		call := &recoveryCall{}
		ctx := context.WithValue(ss.Context(), recoveryCallKey{cfg: cfg}, call)
		defer func() {
			if r := recover(); r != nil {
				err = handlePanic(ctx, r, info.FullMethod, cfg)
			}
		}()
		err = handler(srv, &tracedServerStream{inner: ss, ctx: ctx})
		if !call.recovered {
			reportInternalError(ctx, err, info.FullMethod, cfg)
		}
		return err
	}
}

// reportInternalError reports err, which the handler of method returned,
// to cfg.Reporter when it is an internal error: its gRPC code is Internal
// or Unknown (an error that is neither a gRPC status nor a context error
// is Unknown; see statusCodeOf), the codes the logging interceptor logs at
// error level. A call its own context ended (the client cancelled, or the
// deadline passed) is not reported. The client gets err unchanged.
func reportInternalError(ctx context.Context, err error, method string, cfg *RecoveryConfig) {
	if err == nil || cfg.Reporter == nil {
		return
	}
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		return
	}
	switch statusCodeOf(err) {
	case codes.Internal, codes.Unknown:
		report(ctx, cfg.Reporter, err, method, false, "")
	}
}

// report hands err, a failure of the call to method, to reporter with the
// ErrorContext trace.NewErrorContext builds from the call's ctx (its
// request, trace and span ids), naming the method; recovered marks a
// recovered panic, with its stack. It returns
// whether the reporter returned normally: a reporter that panics is
// contained, since reporting must never fail the call or crash the server.
func report(ctx context.Context, reporter contract.Reporter, err error, method string, recovered bool, stack string) (reported bool) {
	defer func() {
		if recover() != nil {
			reported = false
		}
	}()
	ec := trace.NewErrorContext(ctx)
	ec.Recovered, ec.PanicStack = recovered, stack
	ec.Extra["method"] = method
	reporter.Report(err, ec)
	return true
}

func handlePanic(ctx context.Context, p interface{}, method string, cfg *RecoveryConfig) error {
	stack := ""
	if cfg.EnableStackTrace {
		stack = string(debug.Stack())
	}

	// Report the panic, or log it when there is no reporter (or the
	// reporter panicked): one entry.
	reported := cfg.Reporter != nil && report(ctx, cfg.Reporter, panicerr.FromRecovered(p), method, true, stack)
	if !reported {
		fields := []interface{}{
			"method", method,
			"panic", p,
		}
		if stack != "" {
			fields = append(fields, "stack", stack)
		}
		// The line is bound to the call's request, trace and span ids.
		fallbacklog.Resolve(cfg.Logger).With(trace.LogFields(ctx)...).Error("gRPC panic recovered", fields...)
	}

	if eventsInstalled(cfg.events) {
		dispatchEvent(ctx, cfg.events, &grpcevents.PanicRecovered{
			EventMeta:  eventmeta.Current(ctx),
			Method:     method,
			Panic:      p,
			StackTrace: stack,
		})
	}

	// Call custom handler if set. The custom handler ALWAYS wins; we return
	// whatever it produces (including a nil error, which swallows the panic).
	// Never silently fall through to the default 500 path on nil, because
	// that would mask the application's explicit intent.
	if cfg.PanicHandler != nil {
		return cfg.PanicHandler(ctx, p)
	}

	return status.Errorf(codes.Internal, "internal server error")
}
