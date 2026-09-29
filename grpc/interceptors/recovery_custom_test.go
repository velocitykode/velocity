package interceptors

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestCallLifecycle_CustomHandlerReturnsExact covers Task 8c: when PanicHandler is
// set, its return value is returned verbatim — even nil — rather than falling
// through to the standard codes.Internal path.
func TestCallLifecycle_CustomHandlerReturnsExact(t *testing.T) {
	customErr := status.Error(codes.FailedPrecondition, "custom")
	pair := CallLifecycle(WithPanicHandler(func(ctx context.Context, p interface{}) error {
		return customErr
	}))

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		panic("boom")
	}

	_, err := pair.Unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Foo"}, handler)
	if err == nil {
		t.Fatal("expected error from recovery, got nil")
	}
	if !errors.Is(err, customErr) && err.Error() != customErr.Error() {
		t.Errorf("expected exact custom error, got %v", err)
	}
}

// TestCallLifecycle_CustomHandlerCanSwallowPanic verifies that a custom handler
// returning nil is honoured — no default fall-through.
func TestCallLifecycle_CustomHandlerCanSwallowPanic(t *testing.T) {
	pair := CallLifecycle(WithPanicHandler(func(ctx context.Context, p interface{}) error {
		return nil // intentional: treat the panic as OK
	}))

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		panic("boom")
	}

	_, err := pair.Unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Foo"}, handler)
	if err != nil {
		t.Fatalf("expected nil error (custom handler swallowed panic), got %v", err)
	}
}

// TestCallLifecycle_NoHandlerUsesDefault verifies the default Internal response is
// returned when no custom handler is set.
func TestCallLifecycle_NoHandlerUsesDefault(t *testing.T) {
	pair := CallLifecycle()

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		panic("boom")
	}

	_, err := pair.Unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Foo"}, handler)
	if err == nil {
		t.Fatal("expected default internal error, got nil")
	}
	if status.Code(err) != codes.Internal {
		t.Errorf("expected codes.Internal, got %v", status.Code(err))
	}
}
