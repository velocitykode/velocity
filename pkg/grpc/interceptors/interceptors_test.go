package interceptors_test

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	grpcpkg "github.com/velocitykode/velocity/pkg/grpc"
	"github.com/velocitykode/velocity/pkg/grpc/interceptors"
)

// mockUnaryServerInfo returns a mock grpc.UnaryServerInfo
func mockUnaryServerInfo(method string) *grpc.UnaryServerInfo {
	return &grpc.UnaryServerInfo{
		FullMethod: method,
	}
}

// mockStreamServerInfo returns a mock grpc.StreamServerInfo
func mockStreamServerInfo(method string) *grpc.StreamServerInfo {
	return &grpc.StreamServerInfo{
		FullMethod: method,
	}
}

// mockServerStream implements grpc.ServerStream for testing
type mockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockServerStream) Context() context.Context {
	return m.ctx
}

// TestRecoveryInterceptor tests the recovery interceptor
func TestRecoveryInterceptor(t *testing.T) {
	t.Run("no panic", func(t *testing.T) {
		pair := interceptors.Recovery()

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return "success", nil
		}

		resp, err := pair.Unary(context.Background(), nil, mockUnaryServerInfo("/test.Service/Method"), handler)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != "success" {
			t.Errorf("expected success, got %v", resp)
		}
	})

	t.Run("panic recovery", func(t *testing.T) {
		pair := interceptors.Recovery()

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			panic("test panic")
		}

		resp, err := pair.Unary(context.Background(), nil, mockUnaryServerInfo("/test.Service/Method"), handler)
		if resp != nil {
			t.Errorf("expected nil response, got %v", resp)
		}
		if err == nil {
			t.Fatal("expected error after panic")
		}

		st, ok := status.FromError(err)
		if !ok {
			t.Fatal("expected gRPC status error")
		}
		if st.Code() != codes.Internal {
			t.Errorf("expected Internal code, got %v", st.Code())
		}
	})

	t.Run("stream panic recovery", func(t *testing.T) {
		pair := interceptors.Recovery()

		handler := func(srv interface{}, stream grpc.ServerStream) error {
			panic("stream panic")
		}

		stream := &mockServerStream{ctx: context.Background()}
		err := pair.Stream(nil, stream, mockStreamServerInfo("/test.Service/StreamMethod"), handler)

		if err == nil {
			t.Fatal("expected error after panic")
		}

		st, ok := status.FromError(err)
		if !ok {
			t.Fatal("expected gRPC status error")
		}
		if st.Code() != codes.Internal {
			t.Errorf("expected Internal code, got %v", st.Code())
		}
	})

	t.Run("with custom panic handler", func(t *testing.T) {
		customErr := status.Error(codes.Unavailable, "custom error")
		pair := interceptors.Recovery(
			interceptors.WithPanicHandler(func(ctx context.Context, p interface{}) error {
				return customErr
			}),
		)

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			panic("test panic")
		}

		_, err := pair.Unary(context.Background(), nil, mockUnaryServerInfo("/test.Service/Method"), handler)
		if err != customErr {
			t.Errorf("expected custom error, got %v", err)
		}
	})

	t.Run("convenience functions", func(t *testing.T) {
		unary := interceptors.RecoveryInterceptor()
		if unary == nil {
			t.Error("RecoveryInterceptor returned nil")
		}

		stream := interceptors.StreamRecoveryInterceptor()
		if stream == nil {
			t.Error("StreamRecoveryInterceptor returned nil")
		}
	})
}

// TestLoggingInterceptor tests the logging interceptor
func TestLoggingInterceptor(t *testing.T) {
	t.Run("logs successful request", func(t *testing.T) {
		pair := interceptors.Logging()

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return "success", nil
		}

		resp, err := pair.Unary(context.Background(), nil, mockUnaryServerInfo("/test.Service/Method"), handler)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != "success" {
			t.Errorf("expected success, got %v", resp)
		}
	})

	t.Run("logs error request", func(t *testing.T) {
		pair := interceptors.Logging()

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return nil, status.Error(codes.NotFound, "not found")
		}

		_, err := pair.Unary(context.Background(), nil, mockUnaryServerInfo("/test.Service/Method"), handler)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("skips health checks", func(t *testing.T) {
		pair := interceptors.Logging(interceptors.WithSkipHealthChecks(true))

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return "success", nil
		}

		resp, err := pair.Unary(context.Background(), nil, mockUnaryServerInfo("/grpc.health.v1.Health/Check"), handler)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != "success" {
			t.Errorf("expected success, got %v", resp)
		}
	})

	t.Run("skips configured methods", func(t *testing.T) {
		pair := interceptors.Logging(interceptors.WithSkipMethods("/test.Service/Noisy"))

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return "success", nil
		}

		resp, err := pair.Unary(context.Background(), nil, mockUnaryServerInfo("/test.Service/Noisy"), handler)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != "success" {
			t.Errorf("expected success, got %v", resp)
		}
	})

	t.Run("stream logging", func(t *testing.T) {
		pair := interceptors.Logging()

		handler := func(srv interface{}, stream grpc.ServerStream) error {
			return nil
		}

		stream := &mockServerStream{ctx: context.Background()}
		err := pair.Stream(nil, stream, mockStreamServerInfo("/test.Service/StreamMethod"), handler)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("convenience functions", func(t *testing.T) {
		unary := interceptors.LoggingInterceptor()
		if unary == nil {
			t.Error("LoggingInterceptor returned nil")
		}

		stream := interceptors.StreamLoggingInterceptor()
		if stream == nil {
			t.Error("StreamLoggingInterceptor returned nil")
		}
	})

	t.Run("with extra fields", func(t *testing.T) {
		pair := interceptors.Logging(interceptors.WithExtraFields(func(ctx context.Context) []interface{} {
			return []interface{}{"custom_field", "custom_value"}
		}))

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return "success", nil
		}

		resp, err := pair.Unary(context.Background(), nil, mockUnaryServerInfo("/test.Service/Method"), handler)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != "success" {
			t.Errorf("expected success, got %v", resp)
		}
	})
}

// mockAuthValidator implements interceptors.AuthValidator for testing
type mockAuthValidator struct {
	claims grpcpkg.Claims
	err    error
}

func (m *mockAuthValidator) ValidateToken(ctx context.Context, token string) (grpcpkg.Claims, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.claims, nil
}

// TestAuthInterceptor tests the auth interceptor
func TestAuthInterceptor(t *testing.T) {
	t.Run("public method bypasses auth", func(t *testing.T) {
		validator := &mockAuthValidator{}
		pair := interceptors.Auth(validator)

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return "success", nil
		}

		resp, err := pair.Unary(context.Background(), nil, mockUnaryServerInfo("/grpc.health.v1.Health/Check"), handler)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != "success" {
			t.Errorf("expected success, got %v", resp)
		}
	})

	t.Run("missing metadata", func(t *testing.T) {
		validator := &mockAuthValidator{
			claims: &grpcpkg.BasicClaims{UserID: 123},
		}
		pair := interceptors.Auth(validator)

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return "success", nil
		}

		_, err := pair.Unary(context.Background(), nil, mockUnaryServerInfo("/test.Service/Method"), handler)
		if err == nil {
			t.Fatal("expected error for missing metadata")
		}

		st, _ := status.FromError(err)
		if st.Code() != codes.Unauthenticated {
			t.Errorf("expected Unauthenticated, got %v", st.Code())
		}
	})

	t.Run("missing authorization header", func(t *testing.T) {
		validator := &mockAuthValidator{
			claims: &grpcpkg.BasicClaims{UserID: 123},
		}
		pair := interceptors.Auth(validator)

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return "success", nil
		}

		ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{})
		_, err := pair.Unary(ctx, nil, mockUnaryServerInfo("/test.Service/Method"), handler)
		if err == nil {
			t.Fatal("expected error for missing authorization")
		}
	})

	t.Run("invalid authorization format", func(t *testing.T) {
		validator := &mockAuthValidator{
			claims: &grpcpkg.BasicClaims{UserID: 123},
		}
		pair := interceptors.Auth(validator)

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return "success", nil
		}

		md := metadata.MD{"authorization": []string{"InvalidFormat token123"}}
		ctx := metadata.NewIncomingContext(context.Background(), md)
		_, err := pair.Unary(ctx, nil, mockUnaryServerInfo("/test.Service/Method"), handler)
		if err == nil {
			t.Fatal("expected error for invalid format")
		}
	})

	t.Run("valid token", func(t *testing.T) {
		claims := &grpcpkg.BasicClaims{UserID: 123, TeamID: 456}
		validator := &mockAuthValidator{claims: claims}
		pair := interceptors.Auth(validator)

		var capturedCtx context.Context
		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			capturedCtx = ctx
			return "success", nil
		}

		md := metadata.MD{"authorization": []string{"Bearer valid-token"}}
		ctx := metadata.NewIncomingContext(context.Background(), md)
		resp, err := pair.Unary(ctx, nil, mockUnaryServerInfo("/test.Service/Method"), handler)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != "success" {
			t.Errorf("expected success, got %v", resp)
		}

		// Verify claims were added to context
		ctxClaims := grpcpkg.ClaimsFromContext(capturedCtx)
		if ctxClaims == nil {
			t.Fatal("expected claims in context")
		}
		if ctxClaims.GetUserID() != 123 {
			t.Errorf("expected user ID 123, got %v", ctxClaims.GetUserID())
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		validator := &mockAuthValidator{err: errors.New("invalid token")}
		pair := interceptors.Auth(validator)

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return "success", nil
		}

		md := metadata.MD{"authorization": []string{"Bearer invalid-token"}}
		ctx := metadata.NewIncomingContext(context.Background(), md)
		_, err := pair.Unary(ctx, nil, mockUnaryServerInfo("/test.Service/Method"), handler)

		if err == nil {
			t.Fatal("expected error for invalid token")
		}

		st, _ := status.FromError(err)
		if st.Code() != codes.Unauthenticated {
			t.Errorf("expected Unauthenticated, got %v", st.Code())
		}
	})

	t.Run("custom public methods", func(t *testing.T) {
		validator := &mockAuthValidator{}
		pair := interceptors.Auth(validator, interceptors.WithPublicMethods("/test.Service/Public"))

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return "success", nil
		}

		resp, err := pair.Unary(context.Background(), nil, mockUnaryServerInfo("/test.Service/Public"), handler)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != "success" {
			t.Errorf("expected success, got %v", resp)
		}
	})

	t.Run("stream auth", func(t *testing.T) {
		claims := &grpcpkg.BasicClaims{UserID: 789}
		validator := &mockAuthValidator{claims: claims}
		pair := interceptors.Auth(validator)

		var capturedCtx context.Context
		handler := func(srv interface{}, stream grpc.ServerStream) error {
			capturedCtx = stream.Context()
			return nil
		}

		md := metadata.MD{"authorization": []string{"Bearer valid-token"}}
		ctx := metadata.NewIncomingContext(context.Background(), md)
		stream := &mockServerStream{ctx: ctx}

		err := pair.Stream(nil, stream, mockStreamServerInfo("/test.Service/StreamMethod"), handler)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		ctxClaims := grpcpkg.ClaimsFromContext(capturedCtx)
		if ctxClaims == nil {
			t.Fatal("expected claims in stream context")
		}
	})

	t.Run("stream public method", func(t *testing.T) {
		validator := &mockAuthValidator{}
		pair := interceptors.Auth(validator)

		handler := func(srv interface{}, stream grpc.ServerStream) error {
			return nil
		}

		stream := &mockServerStream{ctx: context.Background()}
		err := pair.Stream(nil, stream, mockStreamServerInfo("/grpc.reflection.v1.ServerReflection/ServerReflectionInfo"), handler)
		if err != nil {
			t.Fatalf("unexpected error for public stream: %v", err)
		}
	})

	t.Run("on auth error callback", func(t *testing.T) {
		var callbackCalled bool
		var callbackMethod string
		validator := &mockAuthValidator{err: errors.New("invalid")}
		pair := interceptors.Auth(validator, interceptors.WithOnAuthError(func(ctx context.Context, method string, err error) {
			callbackCalled = true
			callbackMethod = method
		}))

		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			return "success", nil
		}

		md := metadata.MD{"authorization": []string{"Bearer bad-token"}}
		ctx := metadata.NewIncomingContext(context.Background(), md)
		pair.Unary(ctx, nil, mockUnaryServerInfo("/test.Service/Method"), handler)

		if !callbackCalled {
			t.Error("OnAuthError callback was not called")
		}
		if callbackMethod != "/test.Service/Method" {
			t.Errorf("expected method /test.Service/Method, got %v", callbackMethod)
		}
	})

	t.Run("convenience functions", func(t *testing.T) {
		validator := &mockAuthValidator{}

		unary := interceptors.AuthInterceptor(validator)
		if unary == nil {
			t.Error("AuthInterceptor returned nil")
		}

		stream := interceptors.StreamAuthInterceptor(validator)
		if stream == nil {
			t.Error("StreamAuthInterceptor returned nil")
		}
	})

	t.Run("custom token extractor", func(t *testing.T) {
		claims := &grpcpkg.BasicClaims{UserID: 999}
		validator := &mockAuthValidator{claims: claims}
		pair := interceptors.Auth(validator, interceptors.WithTokenExtractor(func(ctx context.Context) (string, error) {
			md, _ := metadata.FromIncomingContext(ctx)
			tokens := md.Get("x-custom-token")
			if len(tokens) == 0 {
				return "", status.Error(codes.Unauthenticated, "missing custom token")
			}
			return tokens[0], nil
		}))

		var capturedCtx context.Context
		handler := func(ctx context.Context, req interface{}) (interface{}, error) {
			capturedCtx = ctx
			return "success", nil
		}

		md := metadata.MD{"x-custom-token": []string{"custom-token-value"}}
		ctx := metadata.NewIncomingContext(context.Background(), md)
		resp, err := pair.Unary(ctx, nil, mockUnaryServerInfo("/test.Service/Method"), handler)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != "success" {
			t.Errorf("expected success, got %v", resp)
		}

		ctxClaims := grpcpkg.ClaimsFromContext(capturedCtx)
		if ctxClaims == nil {
			t.Fatal("expected claims in context")
		}
		if ctxClaims.GetUserID() != 999 {
			t.Errorf("expected user ID 999, got %v", ctxClaims.GetUserID())
		}
	})
}

// TestInterceptorPair tests the InterceptorPair type
func TestInterceptorPair(t *testing.T) {
	pair := interceptors.InterceptorPair{
		Unary: func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
			return handler(ctx, req)
		},
		Stream: func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			return handler(srv, ss)
		},
	}

	if pair.Unary == nil {
		t.Error("Unary should not be nil")
	}
	if pair.Stream == nil {
		t.Error("Stream should not be nil")
	}
}
