package interceptors

import (
	"context"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/pkg/log"
)

// RecoveryConfig configures the recovery interceptor
type RecoveryConfig struct {
	// Logger is the logger to use. Defaults to the global logger.
	Logger log.Logger

	// EnableStackTrace enables logging of stack traces on panic
	EnableStackTrace bool

	// PanicHandler is an optional custom handler for panics.
	// If set, it's called before the standard error response is returned.
	// Return an error to override the default internal error response.
	PanicHandler func(ctx context.Context, p interface{}) error
}

// RecoveryOption configures recovery behavior
type RecoveryOption func(*RecoveryConfig)

// WithRecoveryLogger sets a custom logger for recovery
func WithRecoveryLogger(logger log.Logger) RecoveryOption {
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

// Recovery creates a recovery interceptor pair that recovers from panics.
// It logs the panic and returns an internal error to the client.
func Recovery(opts ...RecoveryOption) InterceptorPair {
	cfg := &RecoveryConfig{
		EnableStackTrace: true,
	}
	for _, opt := range opts {
		opt(cfg)
	}

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

func recoveryUnary(cfg *RecoveryConfig) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = handlePanic(ctx, r, info.FullMethod, cfg)
			}
		}()
		return handler(ctx, req)
	}
}

func recoveryStream(cfg *RecoveryConfig) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = handlePanic(ss.Context(), r, info.FullMethod, cfg)
			}
		}()
		return handler(srv, ss)
	}
}

func handlePanic(ctx context.Context, p interface{}, method string, cfg *RecoveryConfig) error {
	logger := cfg.Logger
	if logger == nil {
		logger = log.Get()
	}

	// Build log fields
	fields := []interface{}{
		"method", method,
		"panic", p,
	}

	if cfg.EnableStackTrace {
		fields = append(fields, "stack", string(debug.Stack()))
	}

	logger.Error("gRPC panic recovered", fields...)

	// Call custom handler if set
	if cfg.PanicHandler != nil {
		if err := cfg.PanicHandler(ctx, p); err != nil {
			return err
		}
	}

	return status.Errorf(codes.Internal, "internal server error")
}
