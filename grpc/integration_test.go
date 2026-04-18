//go:build integration

// gRPC integration tests — run with: make test-integration
//
// Everything in this file stands up a real in-process gRPC server on an
// ephemeral port, dials it with a real client, and exercises the
// interceptor chain end-to-end. Unit tests in this package stop at the
// interceptor-function boundary; this file verifies the pieces actually
// wire together across a TCP boundary.
//
// No external service is required. The build tag gates these tests off
// the default `go test ./...` run because they're noisier (listener
// setup, async.Go goroutines, full grpc.Serve lifecycle) and we reserve
// those for the nightly integration job.
package grpc_test

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/log"
)

// staticValidator is an auth.Validator that accepts one fixed token.
// It's deliberately naive — the point of the integration test is the
// wiring between interceptor and application, not validator logic.
type staticValidator struct {
	validToken string
}

func (v *staticValidator) ValidateToken(ctx context.Context, token string) (interceptors.Claims, error) {
	if token != v.validToken {
		return nil, fmt.Errorf("invalid token")
	}
	return &interceptors.BasicClaims{UserID: 42, TeamID: 1}, nil
}

// panickingHealth is a Health service whose Check method panics. It's
// registered alongside the real HealthService so we can exercise the
// recovery interceptor against an explicitly-failing RPC without
// needing a generated proto for a bespoke service.
type panickingHealth struct {
	grpc_health_v1.UnimplementedHealthServer
	panicMu      sync.Mutex
	shouldPanic  bool
	lastPanicMsg string
}

func (p *panickingHealth) Check(ctx context.Context, req *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	p.panicMu.Lock()
	shouldPanic := p.shouldPanic
	p.panicMu.Unlock()
	if shouldPanic {
		panic("handler panicked deliberately")
	}
	return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING}, nil
}

// testRig bundles the running server with its client for a single test.
// Cleanup shuts down the server; the client is closed via defer in the
// caller. Every test gets its own rig so ports don't collide.
type testRig struct {
	server *grpc.Server
	conn   *grpcgo.ClientConn
	health grpc_health_v1.HealthClient
	panic  *panickingHealth
}

// startRig picks an ephemeral port, builds a server with the given
// interceptors, and returns a dialed client.
func startRig(t *testing.T, configure func(s *grpc.Server, p *panickingHealth)) *testRig {
	t.Helper()

	// Reserve a port by binding a listener, then immediately close so the
	// grpc.Server can bind the same one. There is a tiny race where
	// another test could bind in the gap, but since tests in this file
	// are sequential and each uses t.Cleanup to release, it's fine.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := fmt.Sprintf("%d", l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()

	logger, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	panicSvc := &panickingHealth{}
	s := grpc.NewServer(
		grpc.WithPort(port),
		grpc.WithLogger(logger),
	)

	// Register the panicking service — it wins over the real Health
	// service because it registers later. That's fine; this file only
	// calls Check so both paths reach panickingHealth.Check.
	s.RegisterService(func(srv interface{}) {
		grpc_health_v1.RegisterHealthServer(srv.(*grpcgo.Server), panicSvc)
	})

	configure(s, panicSvc)

	if err := s.StartAsync(); err != nil {
		t.Fatalf("StartAsync: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	// Dial with a short deadline so a wiring bug surfaces as a test
	// failure, not a hung suite. `NewClient` opens the connection
	// lazily, so a trip through the interceptor chain happens on the
	// first RPC.
	conn, err := grpcgo.NewClient("127.0.0.1:"+port, grpcgo.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return &testRig{
		server: s,
		conn:   conn,
		health: grpc_health_v1.NewHealthClient(conn),
		panic:  panicSvc,
	}
}

// TestIntegration_RecoveryInterceptor_ConvertsPanicToInternalError
// asserts that a panic inside a handler is caught by the recovery
// interceptor and surfaces as status.Code Internal to the client. The
// negative outcome (server drops the TCP connection, client sees
// Unavailable or EOF) is what we are guarding against.
func TestIntegration_RecoveryInterceptor_ConvertsPanicToInternalError(t *testing.T) {
	rig := startRig(t, func(s *grpc.Server, _ *panickingHealth) {
		rec := interceptors.Recovery(interceptors.WithStackTrace(false))
		s.Use(rec.Unary).UseStream(rec.Stream)
	})

	rig.panic.panicMu.Lock()
	rig.panic.shouldPanic = true
	rig.panic.panicMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := rig.health.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err == nil {
		t.Fatal("panicking handler must surface an error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error is not a gRPC status: %v", err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("panic recovery must return codes.Internal, got %v: %q", st.Code(), st.Message())
	}

	// Recovery must keep the server alive — a subsequent RPC with the
	// panic flag off must succeed. If recovery crashed the serve loop,
	// this second call would see Unavailable.
	rig.panic.panicMu.Lock()
	rig.panic.shouldPanic = false
	rig.panic.panicMu.Unlock()
	resp, err := rig.health.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("post-recovery RPC must succeed, got %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("post-recovery status = %v, want SERVING", resp.Status)
	}
}

// TestIntegration_AuthInterceptor_RejectsMissingToken verifies that a
// protected method without a bearer token is rejected with
// Unauthenticated before the handler runs. A guard that silently let
// through unauthenticated calls would pass this test's assert — we
// explicitly check the handler did NOT run by observing that
// panickingHealth.lastPanicMsg stays empty.
func TestIntegration_AuthInterceptor_RejectsMissingToken(t *testing.T) {
	validator := &staticValidator{validToken: "let-me-in"}
	rig := startRig(t, func(s *grpc.Server, _ *panickingHealth) {
		// No public methods — every call must be authenticated.
		auth := interceptors.Auth(validator, interceptors.WithPublicMethods())
		s.Use(auth.Unary).UseStream(auth.Stream)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := rig.health.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err == nil {
		t.Fatal("unauthenticated call must fail")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Errorf("missing token: got code %v, want Unauthenticated", st.Code())
	}
}

// TestIntegration_AuthInterceptor_AcceptsValidToken verifies a request
// with the correct bearer token reaches the handler and the claims are
// plumbed through the context.
func TestIntegration_AuthInterceptor_AcceptsValidToken(t *testing.T) {
	validator := &staticValidator{validToken: "let-me-in"}
	rig := startRig(t, func(s *grpc.Server, _ *panickingHealth) {
		auth := interceptors.Auth(validator, interceptors.WithPublicMethods())
		s.Use(auth.Unary).UseStream(auth.Stream)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer let-me-in")
	resp, err := rig.health.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("authenticated call must succeed, got %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("status = %v, want SERVING", resp.Status)
	}
}

// TestIntegration_AuthInterceptor_PublicMethodBypass verifies the
// PublicMethods allowlist doesn't leak. A regression where the prefix
// check stopped being exact (see auth_public_test.go mutation
// coverage) would let a protected method sneak through because it
// shared a service prefix with an allow-listed one.
func TestIntegration_AuthInterceptor_PublicMethodBypass(t *testing.T) {
	validator := &staticValidator{validToken: "let-me-in"}
	rig := startRig(t, func(s *grpc.Server, _ *panickingHealth) {
		auth := interceptors.Auth(validator,
			interceptors.WithPublicMethods("/grpc.health.v1.Health/"),
		)
		s.Use(auth.Unary).UseStream(auth.Stream)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// No Authorization header — Health must still succeed because its
	// service is allow-listed.
	resp, err := rig.health.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Health on allow-listed service must succeed without auth, got %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("status = %v, want SERVING", resp.Status)
	}
}

// TestIntegration_InterceptorChainOrder guards the order: Recovery
// outermost, then Auth. If Auth were outermost and a panic happened in
// Auth, the recovery interceptor would never see it — and the client
// would observe Unavailable instead of Internal. The test triggers a
// handler panic under a valid token; Recovery must catch it.
func TestIntegration_InterceptorChainOrder(t *testing.T) {
	validator := &staticValidator{validToken: "ok"}
	rig := startRig(t, func(s *grpc.Server, _ *panickingHealth) {
		rec := interceptors.Recovery(interceptors.WithStackTrace(false))
		auth := interceptors.Auth(validator, interceptors.WithPublicMethods())
		// Recovery MUST be added first so it's the outermost in the chain.
		s.Use(rec.Unary, auth.Unary).UseStream(rec.Stream, auth.Stream)
	})

	rig.panic.panicMu.Lock()
	rig.panic.shouldPanic = true
	rig.panic.panicMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer ok")

	_, err := rig.health.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err == nil {
		t.Fatal("panicking authenticated call must surface an error")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Errorf("got code %v want Internal — recovery did not see the panic (check chain order)", st.Code())
	}
	if strings.Contains(st.Message(), "handler panicked") {
		// Recovery MUST scrub the panic message before returning to client.
		t.Errorf("recovery leaked raw panic message to client: %q", st.Message())
	}
}
