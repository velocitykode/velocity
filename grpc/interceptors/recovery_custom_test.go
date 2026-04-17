package interceptors

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestRecovery_CustomHandlerReturnsExact covers Task 8c: when PanicHandler is
// set, its return value is returned verbatim — even nil — rather than falling
// through to the standard codes.Internal path.
func TestRecovery_CustomHandlerReturnsExact(t *testing.T) {
	customErr := status.Error(codes.FailedPrecondition, "custom")
	pair := Recovery(WithPanicHandler(func(ctx context.Context, p interface{}) error {
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

// TestRecovery_CustomHandlerCanSwallowPanic verifies that a custom handler
// returning nil is honoured — no default fall-through.
func TestRecovery_CustomHandlerCanSwallowPanic(t *testing.T) {
	pair := Recovery(WithPanicHandler(func(ctx context.Context, p interface{}) error {
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

// TestRecovery_NoHandlerUsesDefault verifies the default Internal response is
// returned when no custom handler is set.
func TestRecovery_NoHandlerUsesDefault(t *testing.T) {
	pair := Recovery()

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
