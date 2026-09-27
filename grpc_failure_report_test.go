package velocity

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	velgrpc "github.com/velocitykode/velocity/grpc"
)

// explodingOrdersMethod is the method explodingOrdersDesc serves.
const explodingOrdersMethod = "/velocity.test.Orders/Ship"

// explodingOrdersDesc is a hand-written service descriptor whose one unary
// method panics, so the test needs no generated code.
var explodingOrdersDesc = grpcgo.ServiceDesc{
	ServiceName: "velocity.test.Orders",
	HandlerType: (*any)(nil),
	Methods: []grpcgo.MethodDesc{{
		MethodName: "Ship",
		Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpcgo.UnaryServerInterceptor) (any, error) {
			in := new(emptypb.Empty)
			if err := dec(in); err != nil {
				return nil, err
			}
			h := func(context.Context, any) (any, error) { panic("order shipping exploded") }
			if interceptor == nil {
				return h(ctx, in)
			}
			return interceptor(ctx, in, &grpcgo.UnaryServerInfo{Server: srv, FullMethod: explodingOrdersMethod}, h)
		},
	}},
}

// grpcOrdersModule builds a gRPC server the way the gen grpc service
// scaffold does (logger and reporter from Services) and serves it on a
// loopback listener.
type grpcOrdersModule struct {
	lis    net.Listener
	server *velgrpc.Server
}

func (m *grpcOrdersModule) Init(s *app.Services) error {
	m.server = velgrpc.NewServer(
		velgrpc.WithListener(m.lis),
		velgrpc.WithEnvironment("testing"),
		velgrpc.WithLogger(s.Log),
		velgrpc.WithReporter(s.Errors),
	)
	m.server.MarkAuthConfigured()
	m.server.RegisterService(func(srv interface{}) {
		srv.(*grpcgo.Server).RegisterService(&explodingOrdersDesc, struct{}{})
	})
	return nil
}

func (m *grpcOrdersModule) Start(*app.Services) error {
	if err := m.server.Build(); err != nil {
		return err
	}
	return m.server.StartAsync()
}

func (m *grpcOrdersModule) Shutdown(ctx context.Context) error { return m.server.Shutdown(ctx) }

// TestGRPCHandlerPanic_ReachesReporterOnce asserts a panicking gRPC handler
// in a bootstrapped app reaches the Reporter chain exactly once, as a
// recovered panic with the method named, while the client gets the same
// codes.Internal status as without a reporter.
func TestGRPCHandlerPanic_ReachesReporterOnce(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	a, err := NewTestApp(WithModules(&grpcOrdersModule{lis: lis}))
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	defer a.Shutdown(context.Background())
	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	reports := &failureReports{}
	reports.add(a)

	conn, err := grpcgo.NewClient(lis.Addr().String(), grpcgo.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = conn.Invoke(ctx, explodingOrdersMethod, &emptypb.Empty{}, &emptypb.Empty{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("status = %v (%v), want codes.Internal", status.Code(err), err)
	}

	if n := reports.count(); n != 1 {
		t.Fatalf("panic reported %d times, want 1 (%v)", n, reports.errs)
	}
	var rp contract.RecoveredPanic
	if !errors.As(reports.errs[0], &rp) || rp.Recovered() != "order shipping exploded" {
		t.Errorf("reported %#v, want the recovered panic", reports.errs[0])
	}
	if got := reports.ctxs[0].Extra["method"]; got != explodingOrdersMethod {
		t.Errorf("report method = %v, want %s", got, explodingOrdersMethod)
	}
	if !reports.ctxs[0].Recovered {
		t.Error("report is not flagged as a recovered panic")
	}
}
