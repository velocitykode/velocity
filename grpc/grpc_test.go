package grpc_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/grpc/converters"
)

func TestServerOptions(t *testing.T) {
	tests := []struct {
		name     string
		opts     []grpc.ServerOption
		wantPort string
		wantRefl bool
	}{
		{
			name:     "default options",
			opts:     nil,
			wantPort: "50051",
			wantRefl: false,
		},
		{
			name: "custom port",
			opts: []grpc.ServerOption{
				grpc.WithPort("9000"),
			},
			wantPort: "9000",
			wantRefl: false,
		},
		{
			name: "with reflection",
			opts: []grpc.ServerOption{
				grpc.WithReflection(true),
			},
			wantPort: "50051",
			wantRefl: true,
		},
		{
			name: "all options",
			opts: []grpc.ServerOption{
				grpc.WithPort("8080"),
				grpc.WithReflection(true),
				grpc.WithMaxRecvMsgSize(8 * 1024 * 1024),
				grpc.WithMaxSendMsgSize(8 * 1024 * 1024),
			},
			wantPort: "8080",
			wantRefl: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := grpc.NewServer(tt.opts...)
			if server.Port() != tt.wantPort {
				t.Errorf("Port() = %v, want %v", server.Port(), tt.wantPort)
			}
		})
	}
}

func TestServerLifecycle(t *testing.T) {
	server := grpc.NewServer(
		grpc.WithPort("0"), // Random available port
		grpc.WithReflection(true),
	)

	// Initially not running
	if server.IsRunning() {
		t.Error("Server should not be running before Start()")
	}

	// Start async
	if err := server.StartAsync(); err != nil {
		t.Fatalf("StartAsync() error = %v", err)
	}

	// Give it time to start
	time.Sleep(100 * time.Millisecond)

	// Should be running now
	if !server.IsRunning() {
		t.Error("Server should be running after StartAsync()")
	}

	// Address should be set
	if server.Address() == "" {
		t.Error("Address() should not be empty after Start()")
	}

	// GRPCServer should be available
	if server.GRPCServer() == nil {
		t.Error("GRPCServer() should not be nil after Build()")
	}

	// Stop the server
	server.Stop()

	// Give it time to stop
	time.Sleep(100 * time.Millisecond)

	// Should not be running
	if server.IsRunning() {
		t.Error("Server should not be running after Stop()")
	}
}

func TestServerDoubleStart(t *testing.T) {
	server := grpc.NewServer(grpc.WithPort("0"))

	if err := server.StartAsync(); err != nil {
		t.Fatalf("First StartAsync() error = %v", err)
	}
	defer server.Stop()

	time.Sleep(50 * time.Millisecond)

	// Second start should fail
	err := server.StartAsync()
	if err != grpc.ErrServerAlreadyRunning {
		t.Errorf("Second StartAsync() error = %v, want %v", err, grpc.ErrServerAlreadyRunning)
	}
}

func TestGatewayOptions(t *testing.T) {
	tests := []struct {
		name         string
		opts         []grpc.GatewayOption
		wantPort     string
		wantEndpoint string
	}{
		{
			name:         "default options",
			opts:         nil,
			wantPort:     "8080",
			wantEndpoint: "",
		},
		{
			name: "custom port",
			opts: []grpc.GatewayOption{
				grpc.GatewayWithPort("9000"),
			},
			wantPort:     "9000",
			wantEndpoint: "",
		},
		{
			name: "with endpoint",
			opts: []grpc.GatewayOption{
				grpc.GatewayWithGRPCEndpoint("localhost:50051"),
			},
			wantPort:     "8080",
			wantEndpoint: "localhost:50051",
		},
		{
			name: "all options",
			opts: []grpc.GatewayOption{
				grpc.GatewayWithPort("8082"),
				grpc.GatewayWithGRPCEndpoint("localhost:50052"),
				grpc.GatewayWithInsecure(),
			},
			wantPort:     "8082",
			wantEndpoint: "localhost:50052",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gateway := grpc.NewGateway(tt.opts...)
			if gateway.Port() != tt.wantPort {
				t.Errorf("Port() = %v, want %v", gateway.Port(), tt.wantPort)
			}
			if gateway.GRPCEndpoint() != tt.wantEndpoint {
				t.Errorf("GRPCEndpoint() = %v, want %v", gateway.GRPCEndpoint(), tt.wantEndpoint)
			}
		})
	}
}

func TestGatewayBuildRequiresTLS(t *testing.T) {
	// GatewayWithTransportConfig with neither TLS nor Insecure should produce an error
	gateway := grpc.NewGateway(
		grpc.GatewayWithGRPCEndpoint("localhost:50051"),
		grpc.GatewayWithTransportConfig(grpc.GatewayTransportConfig{}),
	)
	err := gateway.Build(context.Background())
	if err == nil {
		t.Error("Build() should fail without TLS config or explicit insecure opt-in")
	}
}

func TestGatewayBuildRequiresEndpoint(t *testing.T) {
	gateway := grpc.NewGateway()
	err := gateway.Build(context.Background())
	if err != grpc.ErrNoEndpoint {
		t.Errorf("Build() error = %v, want %v", err, grpc.ErrNoEndpoint)
	}
}

func TestContext(t *testing.T) {
	t.Run("claims", func(t *testing.T) {
		ctx := context.Background()

		// No claims initially
		if grpc.ClaimsFromContext(ctx) != nil {
			t.Error("ClaimsFromContext should return nil for empty context")
		}

		// Add claims
		claims := &grpc.BasicClaims{UserID: 123, TeamID: 456}
		ctx = grpc.ContextWithClaims(ctx, claims)

		// Should retrieve claims
		got := grpc.ClaimsFromContext(ctx)
		if got == nil {
			t.Fatal("ClaimsFromContext returned nil")
		}
		if got.GetUserID() != 123 {
			t.Errorf("GetUserID() = %v, want 123", got.GetUserID())
		}
		if got.GetTeamID() != 456 {
			t.Errorf("GetTeamID() = %v, want 456", got.GetTeamID())
		}
	})

	t.Run("user and team id helpers", func(t *testing.T) {
		ctx := context.Background()

		// No claims - should return 0
		if grpc.UserIDFromContext(ctx) != 0 {
			t.Error("UserIDFromContext should return 0 for empty context")
		}
		if grpc.TeamIDFromContext(ctx) != 0 {
			t.Error("TeamIDFromContext should return 0 for empty context")
		}

		// With claims
		claims := &grpc.BasicClaims{UserID: 789, TeamID: 101}
		ctx = grpc.ContextWithClaims(ctx, claims)

		if grpc.UserIDFromContext(ctx) != 789 {
			t.Errorf("UserIDFromContext() = %v, want 789", grpc.UserIDFromContext(ctx))
		}
		if grpc.TeamIDFromContext(ctx) != 101 {
			t.Errorf("TeamIDFromContext() = %v, want 101", grpc.TeamIDFromContext(ctx))
		}
	})

	t.Run("request id", func(t *testing.T) {
		ctx := context.Background()

		// No request ID initially
		if grpc.RequestIDFromContext(ctx) != "" {
			t.Error("RequestIDFromContext should return empty string for empty context")
		}

		// Add request ID
		ctx = grpc.ContextWithRequestID(ctx, "req-123")

		if grpc.RequestIDFromContext(ctx) != "req-123" {
			t.Errorf("RequestIDFromContext() = %v, want req-123", grpc.RequestIDFromContext(ctx))
		}
	})

	t.Run("method", func(t *testing.T) {
		ctx := context.Background()

		// No method initially
		if grpc.MethodFromContext(ctx) != "" {
			t.Error("MethodFromContext should return empty string for empty context")
		}

		// Add method
		ctx = grpc.ContextWithMethod(ctx, "/mypackage.MyService/MyMethod")

		if grpc.MethodFromContext(ctx) != "/mypackage.MyService/MyMethod" {
			t.Errorf("MethodFromContext() = %v, want /mypackage.MyService/MyMethod", grpc.MethodFromContext(ctx))
		}
	})

	t.Run("generate request id", func(t *testing.T) {
		id1 := grpc.GenerateRequestID()
		id2 := grpc.GenerateRequestID()

		if id1 == "" {
			t.Error("GenerateRequestID returned empty string")
		}
		if id1 == id2 {
			t.Error("GenerateRequestID should return unique IDs")
		}
	})

	t.Run("must claims from context panics", func(t *testing.T) {
		ctx := context.Background()

		defer func() {
			if r := recover(); r == nil {
				t.Error("MustClaimsFromContext should panic when no claims")
			}
		}()

		grpc.MustClaimsFromContext(ctx)
	})
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode codes.Code
	}{
		{"unauthenticated", grpc.ErrUnauthenticated, codes.Unauthenticated},
		{"permission denied", grpc.ErrPermissionDenied, codes.PermissionDenied},
		{"not found", grpc.ErrNotFound, codes.NotFound},
		{"already exists", grpc.ErrAlreadyExists, codes.AlreadyExists},
		{"invalid argument", grpc.ErrInvalidArgument, codes.InvalidArgument},
		{"internal", grpc.ErrInternal, codes.Internal},
		{"unimplemented", grpc.ErrUnimplemented, codes.Unimplemented},
		{"unavailable", grpc.ErrUnavailable, codes.Unavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := grpc.Code(tt.err)
			if code != tt.wantCode {
				t.Errorf("Code() = %v, want %v", code, tt.wantCode)
			}
		})
	}
}

func TestErrorHelpers(t *testing.T) {
	t.Run("Unauthenticated", func(t *testing.T) {
		err := grpc.Unauthenticated("custom message")
		if grpc.Code(err) != codes.Unauthenticated {
			t.Errorf("Code() = %v, want Unauthenticated", grpc.Code(err))
		}
		if grpc.Message(err) != "custom message" {
			t.Errorf("Message() = %v, want custom message", grpc.Message(err))
		}
	})

	t.Run("NotFoundf", func(t *testing.T) {
		err := grpc.NotFoundf("user %d not found", 123)
		if grpc.Code(err) != codes.NotFound {
			t.Errorf("Code() = %v, want NotFound", grpc.Code(err))
		}
		if grpc.Message(err) != "user 123 not found" {
			t.Errorf("Message() = %v, want user 123 not found", grpc.Message(err))
		}
	})

	t.Run("InvalidArgumentf", func(t *testing.T) {
		err := grpc.InvalidArgumentf("field %s is required", "name")
		if grpc.Code(err) != codes.InvalidArgument {
			t.Errorf("Code() = %v, want InvalidArgument", grpc.Code(err))
		}
	})

	t.Run("WrapError", func(t *testing.T) {
		// Nil error
		if grpc.WrapError(nil) != nil {
			t.Error("WrapError(nil) should return nil")
		}

		// Regular error
		err := grpc.WrapError(context.Canceled)
		if grpc.Code(err) != codes.Internal {
			t.Errorf("WrapError(regular error) Code() = %v, want Internal", grpc.Code(err))
		}

		// Already a gRPC error
		grpcErr := grpc.NotFound("test")
		wrapped := grpc.WrapError(grpcErr)
		if grpc.Code(wrapped) != codes.NotFound {
			t.Errorf("WrapError(grpc error) Code() = %v, want NotFound", grpc.Code(wrapped))
		}
	})

	t.Run("WrapErrorWithCode", func(t *testing.T) {
		err := grpc.WrapErrorWithCode(context.Canceled, codes.Unavailable)
		if grpc.Code(err) != codes.Unavailable {
			t.Errorf("Code() = %v, want Unavailable", grpc.Code(err))
		}
	})

	t.Run("Code for nil and unknown", func(t *testing.T) {
		if grpc.Code(nil) != codes.OK {
			t.Errorf("Code(nil) = %v, want OK", grpc.Code(nil))
		}

		regularErr := context.Canceled
		if grpc.Code(regularErr) != codes.Unknown {
			t.Errorf("Code(regular error) = %v, want Unknown", grpc.Code(regularErr))
		}
	})

	t.Run("Message", func(t *testing.T) {
		if grpc.Message(nil) != "" {
			t.Errorf("Message(nil) = %v, want empty", grpc.Message(nil))
		}

		regularErr := context.Canceled
		if grpc.Message(regularErr) == "" {
			t.Error("Message(regular error) should not be empty")
		}
	})

	t.Run("IsCode helpers", func(t *testing.T) {
		notFound := grpc.NotFound("test")
		unauth := grpc.Unauthenticated("test")
		permDenied := grpc.PermissionDenied("test")
		invalidArg := grpc.InvalidArgument("test")
		internal := grpc.Internal("test")
		unavailable := grpc.Unavailable("test")

		if !grpc.IsNotFound(notFound) {
			t.Error("IsNotFound should return true for NotFound error")
		}
		if grpc.IsNotFound(unauth) {
			t.Error("IsNotFound should return false for Unauthenticated error")
		}

		if !grpc.IsUnauthenticated(unauth) {
			t.Error("IsUnauthenticated should return true")
		}
		if !grpc.IsPermissionDenied(permDenied) {
			t.Error("IsPermissionDenied should return true")
		}
		if !grpc.IsInvalidArgument(invalidArg) {
			t.Error("IsInvalidArgument should return true")
		}
		if !grpc.IsInternal(internal) {
			t.Error("IsInternal should return true")
		}
		if !grpc.IsUnavailable(unavailable) {
			t.Error("IsUnavailable should return true")
		}
	})

	t.Run("FromError", func(t *testing.T) {
		err := grpc.NotFound("test")
		s := grpc.FromError(err)
		if s == nil {
			t.Fatal("FromError returned nil")
		}
		if s.Code() != codes.NotFound {
			t.Errorf("FromError().Code() = %v, want NotFound", s.Code())
		}
	})
}

func TestHealthService(t *testing.T) {
	t.Run("default status", func(t *testing.T) {
		hs := grpc.NewHealthService()

		resp, err := hs.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
		if err != nil {
			t.Fatalf("Check() error = %v", err)
		}
		if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
			t.Errorf("Status = %v, want SERVING", resp.Status)
		}
	})

	t.Run("set serving status", func(t *testing.T) {
		hs := grpc.NewHealthService()

		hs.SetNotServing("myservice")

		resp, err := hs.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{
			Service: "myservice",
		})
		if err != nil {
			t.Fatalf("Check() error = %v", err)
		}
		if resp.Status != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
			t.Errorf("Status = %v, want NOT_SERVING", resp.Status)
		}

		hs.SetServing("myservice")

		resp, err = hs.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{
			Service: "myservice",
		})
		if err != nil {
			t.Fatalf("Check() error = %v", err)
		}
		if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
			t.Errorf("Status = %v, want SERVING", resp.Status)
		}
	})

	t.Run("unknown service", func(t *testing.T) {
		hs := grpc.NewHealthService()

		resp, err := hs.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{
			Service: "unknown",
		})
		if err != nil {
			t.Fatalf("Check() error = %v", err)
		}
		if resp.Status != grpc_health_v1.HealthCheckResponse_SERVICE_UNKNOWN {
			t.Errorf("Status = %v, want SERVICE_UNKNOWN", resp.Status)
		}
	})

	t.Run("dynamic checker", func(t *testing.T) {
		hs := grpc.NewHealthService()

		healthy := true
		hs.RegisterChecker("dynamic", func(ctx context.Context) error {
			if healthy {
				return nil
			}
			return status.Error(codes.Unavailable, "not healthy")
		})

		// Should be healthy
		resp, _ := hs.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{
			Service: "dynamic",
		})
		if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
			t.Errorf("Status = %v, want SERVING", resp.Status)
		}

		// Set unhealthy
		healthy = false
		resp, _ = hs.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{
			Service: "dynamic",
		})
		if resp.Status != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
			t.Errorf("Status = %v, want NOT_SERVING", resp.Status)
		}

		// Remove checker
		hs.RemoveChecker("dynamic")
		resp, _ = hs.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{
			Service: "dynamic",
		})
		if resp.Status != grpc_health_v1.HealthCheckResponse_SERVICE_UNKNOWN {
			t.Errorf("Status = %v, want SERVICE_UNKNOWN", resp.Status)
		}
	})

	t.Run("get status", func(t *testing.T) {
		hs := grpc.NewHealthService()

		status, ok := hs.GetStatus("")
		if !ok || status != grpc_health_v1.HealthCheckResponse_SERVING {
			t.Error("GetStatus for empty service should return SERVING")
		}

		_, ok = hs.GetStatus("unknown")
		if ok {
			t.Error("GetStatus for unknown service should return false")
		}
	})
}

func TestLoadConfig(t *testing.T) {
	t.Run("environment config", func(t *testing.T) {
		t.Setenv("GRPC_PORT", "50052")
		t.Setenv("GRPC_REFLECTION", "true")
		t.Setenv("GATEWAY_PORT", "8082")
		t.Setenv("GRPC_ENDPOINT", "localhost:50052")

		cfg := grpc.LoadConfig()
		if cfg.ServerPort != "50052" {
			t.Errorf("ServerPort = %v, want 50052", cfg.ServerPort)
		}
		if !cfg.EnableReflection {
			t.Error("EnableReflection should be true")
		}
		if cfg.GatewayPort != "8082" {
			t.Errorf("GatewayPort = %v, want 8082", cfg.GatewayPort)
		}
		if cfg.GRPCEndpoint != "localhost:50052" {
			t.Errorf("GRPCEndpoint = %v, want localhost:50052", cfg.GRPCEndpoint)
		}
	})

	t.Run("default config", func(t *testing.T) {
		t.Setenv("GRPC_PORT", "")
		t.Setenv("GRPC_REFLECTION", "")
		cfg := grpc.LoadConfig()
		if cfg.ServerPort != "50051" {
			t.Errorf("ServerPort = %v, want 50051", cfg.ServerPort)
		}
		if cfg.EnableReflection {
			t.Error("EnableReflection should be false by default")
		}
	})
}

func TestTimeConverters(t *testing.T) {
	t.Run("TimeToProto nil", func(t *testing.T) {
		result := converters.TimeToProto(nil)
		if result != nil {
			t.Error("TimeToProto(nil) should return nil")
		}
	})

	t.Run("TimeToProto value", func(t *testing.T) {
		now := time.Now()
		result := converters.TimeToProto(&now)
		if result == nil {
			t.Fatal("TimeToProto returned nil")
		}
		if !result.AsTime().Equal(now) {
			t.Error("TimeToProto did not preserve time")
		}
	})

	t.Run("TimeValueToProto", func(t *testing.T) {
		now := time.Now()
		result := converters.TimeValueToProto(now)
		if result == nil {
			t.Fatal("TimeValueToProto returned nil")
		}
		if !result.AsTime().Equal(now) {
			t.Error("TimeValueToProto did not preserve time")
		}
	})

	t.Run("ProtoToTime nil", func(t *testing.T) {
		result := converters.ProtoToTime(nil)
		if result != nil {
			t.Error("ProtoToTime(nil) should return nil")
		}
	})

	t.Run("ProtoToTime value", func(t *testing.T) {
		now := time.Now()
		proto := converters.TimeValueToProto(now)
		result := converters.ProtoToTime(proto)
		if result == nil {
			t.Fatal("ProtoToTime returned nil")
		}
		// Compare with some tolerance for nanosecond precision
		if now.Sub(*result).Abs() > time.Microsecond {
			t.Error("ProtoToTime did not preserve time")
		}
	})

	t.Run("ProtoToTimeValue nil", func(t *testing.T) {
		result := converters.ProtoToTimeValue(nil)
		if !result.IsZero() {
			t.Error("ProtoToTimeValue(nil) should return zero time")
		}
	})

	t.Run("NowProto", func(t *testing.T) {
		before := time.Now()
		result := converters.NowProto()
		after := time.Now()

		if result == nil {
			t.Fatal("NowProto returned nil")
		}
		ts := result.AsTime()
		if ts.Before(before) || ts.After(after) {
			t.Error("NowProto time is outside expected range")
		}
	})

	t.Run("DurationToProto", func(t *testing.T) {
		d := 5 * time.Second
		result := converters.DurationToProto(d)
		if result == nil {
			t.Fatal("DurationToProto returned nil")
		}
		if result.AsDuration() != d {
			t.Error("DurationToProto did not preserve duration")
		}
	})

	t.Run("ProtoToDuration nil", func(t *testing.T) {
		result := converters.ProtoToDuration(nil)
		if result != 0 {
			t.Error("ProtoToDuration(nil) should return 0")
		}
	})

	t.Run("TimeOrNil", func(t *testing.T) {
		// Zero time
		zero := time.Time{}
		if converters.TimeOrNil(zero) != nil {
			t.Error("TimeOrNil(zero) should return nil")
		}

		// Non-zero time
		now := time.Now()
		result := converters.TimeOrNil(now)
		if result == nil {
			t.Error("TimeOrNil(now) should not return nil")
		}
	})

	t.Run("TimeOrZero", func(t *testing.T) {
		// Nil pointer
		result := converters.TimeOrZero(nil)
		if !result.IsZero() {
			t.Error("TimeOrZero(nil) should return zero time")
		}

		// Non-nil pointer
		now := time.Now()
		result = converters.TimeOrZero(&now)
		if !result.Equal(now) {
			t.Error("TimeOrZero should return the time value")
		}
	})
}

func TestPaginationConverters(t *testing.T) {
	t.Run("NormalizePagination default", func(t *testing.T) {
		p := converters.NormalizePagination(0, 0)
		if p.Page != 1 {
			t.Errorf("Page = %v, want 1", p.Page)
		}
		if p.Size != converters.DefaultPageSize {
			t.Errorf("Size = %v, want %v", p.Size, converters.DefaultPageSize)
		}
		if p.Offset != 0 {
			t.Errorf("Offset = %v, want 0", p.Offset)
		}
	})

	t.Run("NormalizePagination custom", func(t *testing.T) {
		p := converters.NormalizePagination(3, 25)
		if p.Page != 3 {
			t.Errorf("Page = %v, want 3", p.Page)
		}
		if p.Size != 25 {
			t.Errorf("Size = %v, want 25", p.Size)
		}
		if p.Offset != 50 { // (3-1) * 25
			t.Errorf("Offset = %v, want 50", p.Offset)
		}
	})

	t.Run("NormalizePagination max size", func(t *testing.T) {
		p := converters.NormalizePagination(1, 1000)
		if p.Size != converters.MaxPageSize {
			t.Errorf("Size = %v, want %v", p.Size, converters.MaxPageSize)
		}
	})

	t.Run("NewPaginationResponse", func(t *testing.T) {
		resp := converters.NewPaginationResponse(2, 10, 55)
		if resp.Page != 2 {
			t.Errorf("Page = %v, want 2", resp.Page)
		}
		if resp.PageSize != 10 {
			t.Errorf("PageSize = %v, want 10", resp.PageSize)
		}
		if resp.TotalItems != 55 {
			t.Errorf("TotalItems = %v, want 55", resp.TotalItems)
		}
		if resp.TotalPages != 6 { // ceil(55/10)
			t.Errorf("TotalPages = %v, want 6", resp.TotalPages)
		}
		if !resp.HasNext {
			t.Error("HasNext should be true")
		}
		if !resp.HasPrev {
			t.Error("HasPrev should be true")
		}
	})

	t.Run("NewPaginationResponse first page", func(t *testing.T) {
		resp := converters.NewPaginationResponse(1, 10, 55)
		if resp.HasPrev {
			t.Error("HasPrev should be false on first page")
		}
	})

	t.Run("NewPaginationResponse last page", func(t *testing.T) {
		resp := converters.NewPaginationResponse(6, 10, 55)
		if resp.HasNext {
			t.Error("HasNext should be false on last page")
		}
	})

	t.Run("CalculateTotalPages", func(t *testing.T) {
		tests := []struct {
			total, pageSize, want int32
		}{
			{0, 10, 1},
			{5, 10, 1},
			{10, 10, 1},
			{11, 10, 2},
			{55, 10, 6},
			{100, 20, 5},
		}

		for _, tt := range tests {
			got := converters.CalculateTotalPages(tt.total, tt.pageSize)
			if got != tt.want {
				t.Errorf("CalculateTotalPages(%d, %d) = %d, want %d",
					tt.total, tt.pageSize, got, tt.want)
			}
		}
	})

	t.Run("NormalizeCursorPagination", func(t *testing.T) {
		p := converters.NormalizeCursorPagination("cursor123", 50)
		if p.Cursor != "cursor123" {
			t.Errorf("Cursor = %v, want cursor123", p.Cursor)
		}
		if p.Limit != 50 {
			t.Errorf("Limit = %v, want 50", p.Limit)
		}

		// Default limit
		p = converters.NormalizeCursorPagination("", 0)
		if p.Limit != converters.DefaultPageSize {
			t.Errorf("Limit = %v, want %v", p.Limit, converters.DefaultPageSize)
		}

		// Max limit
		p = converters.NormalizeCursorPagination("", 1000)
		if p.Limit != converters.MaxPageSize {
			t.Errorf("Limit = %v, want %v", p.Limit, converters.MaxPageSize)
		}
	})

	t.Run("NewCursorResponse", func(t *testing.T) {
		resp := converters.NewCursorResponse("next", "prev", true, 25)
		if resp.NextCursor != "next" {
			t.Errorf("NextCursor = %v, want next", resp.NextCursor)
		}
		if resp.PrevCursor != "prev" {
			t.Errorf("PrevCursor = %v, want prev", resp.PrevCursor)
		}
		if !resp.HasMore {
			t.Error("HasMore should be true")
		}
		if resp.Limit != 25 {
			t.Errorf("Limit = %v, want 25", resp.Limit)
		}
	})
}
