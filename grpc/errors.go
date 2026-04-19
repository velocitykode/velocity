package grpc

import (
	"errors"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	debugMu   sync.RWMutex
	debugMode bool
)

// SetDebugMode enables or disables debug mode for gRPC error handling.
// When enabled, internal error messages are exposed to clients for developer convenience.
// Must be called during application initialization.
func SetDebugMode(v bool) {
	debugMu.Lock()
	defer debugMu.Unlock()
	debugMode = v
}

// Common gRPC errors with standard messages
var (
	ErrUnauthenticated    = status.Error(codes.Unauthenticated, "authentication required")
	ErrPermissionDenied   = status.Error(codes.PermissionDenied, "permission denied")
	ErrNotFound           = status.Error(codes.NotFound, "resource not found")
	ErrAlreadyExists      = status.Error(codes.AlreadyExists, "resource already exists")
	ErrInvalidArgument    = status.Error(codes.InvalidArgument, "invalid argument")
	ErrInternal           = status.Error(codes.Internal, "internal server error")
	ErrUnimplemented      = status.Error(codes.Unimplemented, "not implemented")
	ErrUnavailable        = status.Error(codes.Unavailable, "service unavailable")
	ErrResourceExhausted  = status.Error(codes.ResourceExhausted, "resource exhausted")
	ErrFailedPrecondition = status.Error(codes.FailedPrecondition, "failed precondition")
	ErrAborted            = status.Error(codes.Aborted, "operation aborted")
	ErrDeadlineExceeded   = status.Error(codes.DeadlineExceeded, "deadline exceeded")
	ErrCancelled          = status.Error(codes.Canceled, "operation cancelled")
)

// NewGRPCError creates a new gRPC status error with the given code and message.
func NewGRPCError(code codes.Code, msg string) error {
	return status.Error(code, msg)
}

// NewGRPCErrorf creates a new gRPC status error with a formatted message.
func NewGRPCErrorf(code codes.Code, format string, args ...interface{}) error {
	return status.Errorf(code, format, args...)
}

// Unauthenticated creates an unauthenticated error with a custom message
func Unauthenticated(msg string) error {
	return status.Error(codes.Unauthenticated, msg)
}

// PermissionDenied creates a permission denied error with a custom message
func PermissionDenied(msg string) error {
	return status.Error(codes.PermissionDenied, msg)
}

// NotFound creates a not found error with a custom message
func NotFound(msg string) error {
	return status.Error(codes.NotFound, msg)
}

// NotFoundf creates a not found error with a formatted message
func NotFoundf(format string, args ...interface{}) error {
	return status.Errorf(codes.NotFound, format, args...)
}

// AlreadyExists creates an already exists error with a custom message
func AlreadyExists(msg string) error {
	return status.Error(codes.AlreadyExists, msg)
}

// InvalidArgument creates an invalid argument error with a custom message
func InvalidArgument(msg string) error {
	return status.Error(codes.InvalidArgument, msg)
}

// InvalidArgumentf creates an invalid argument error with a formatted message
func InvalidArgumentf(format string, args ...interface{}) error {
	return status.Errorf(codes.InvalidArgument, format, args...)
}

// Internal creates an internal error with a custom message
func Internal(msg string) error {
	return status.Error(codes.Internal, msg)
}

// Internalf creates an internal error with a formatted message
func Internalf(format string, args ...interface{}) error {
	return status.Errorf(codes.Internal, format, args...)
}

// FailedPrecondition creates a failed precondition error with a custom message
func FailedPrecondition(msg string) error {
	return status.Error(codes.FailedPrecondition, msg)
}

// ResourceExhausted creates a resource exhausted error with a custom message
func ResourceExhausted(msg string) error {
	return status.Error(codes.ResourceExhausted, msg)
}

// Unavailable creates an unavailable error with a custom message
func Unavailable(msg string) error {
	return status.Error(codes.Unavailable, msg)
}

// isDebugMode returns true when the application is running in debug mode
func isDebugMode() bool {
	debugMu.RLock()
	defer debugMu.RUnlock()
	return debugMode
}

// WrapError wraps a Go error in a gRPC status error.
// If the error is already a gRPC status error, it is returned unchanged.
// Otherwise, it's wrapped as an internal error with a generic message.
// In debug mode, the original message is preserved for developer convenience.
func WrapError(err error) error {
	if err == nil {
		return nil
	}

	// Check if it's already a gRPC status error
	if _, ok := status.FromError(err); ok {
		return err
	}

	if isDebugMode() {
		return status.Error(codes.Internal, err.Error())
	}
	return status.Error(codes.Internal, "internal server error")
}

// WrapErrorWithCode wraps a Go error with a specific gRPC code.
// If the error is already a gRPC status error, it is returned unchanged.
// For Internal/Unknown codes, the raw message is hidden from clients
// unless debug mode is enabled.
func WrapErrorWithCode(err error, code codes.Code) error {
	if err == nil {
		return nil
	}

	// Check if it's already a gRPC status error
	if _, ok := status.FromError(err); ok {
		return err
	}

	// For internal-class errors, hide details from clients
	if code == codes.Internal || code == codes.Unknown {
		if !isDebugMode() {
			return status.Error(code, "internal server error")
		}
	}

	return status.Error(code, err.Error())
}

// Code extracts the gRPC status code from an error.
// Returns codes.OK if the error is nil.
// Returns codes.Unknown if the error is not a gRPC status error.
func Code(err error) codes.Code {
	if err == nil {
		return codes.OK
	}

	if s, ok := status.FromError(err); ok {
		return s.Code()
	}

	return codes.Unknown
}

// Message extracts the message from a gRPC status error.
// Returns empty string if the error is nil.
// Returns the error string if it's not a gRPC status error.
func Message(err error) string {
	if err == nil {
		return ""
	}

	if s, ok := status.FromError(err); ok {
		return s.Message()
	}

	return err.Error()
}

// IsCode checks if an error has a specific gRPC status code
func IsCode(err error, code codes.Code) bool {
	return Code(err) == code
}

// IsNotFound checks if an error is a NotFound error
func IsNotFound(err error) bool {
	return IsCode(err, codes.NotFound)
}

// IsUnauthenticated checks if an error is an Unauthenticated error
func IsUnauthenticated(err error) bool {
	return IsCode(err, codes.Unauthenticated)
}

// IsPermissionDenied checks if an error is a PermissionDenied error
func IsPermissionDenied(err error) bool {
	return IsCode(err, codes.PermissionDenied)
}

// IsInvalidArgument checks if an error is an InvalidArgument error
func IsInvalidArgument(err error) bool {
	return IsCode(err, codes.InvalidArgument)
}

// IsInternal checks if an error is an Internal error
func IsInternal(err error) bool {
	return IsCode(err, codes.Internal)
}

// IsUnavailable checks if an error is an Unavailable error
func IsUnavailable(err error) bool {
	return IsCode(err, codes.Unavailable)
}

// FromError converts a standard Go error to a gRPC status.
// This is useful for extracting code and message from errors.
func FromError(err error) *status.Status {
	s, _ := status.FromError(err)
	return s
}

// ErrorIs checks if a gRPC error matches a target error by comparing codes and messages
func ErrorIs(err, target error) bool {
	if err == nil || target == nil {
		return err == target
	}

	errStatus, errOk := status.FromError(err)
	targetStatus, targetOk := status.FromError(target)

	if errOk && targetOk {
		return errStatus.Code() == targetStatus.Code() &&
			errStatus.Message() == targetStatus.Message()
	}

	return errors.Is(err, target)
}
