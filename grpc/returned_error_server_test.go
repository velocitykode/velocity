package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/log"
)

// poisonedError is a handler's returned error whose methods panic.
type poisonedError struct{}

func (poisonedError) Error() string              { panic("Error broke") }
func (poisonedError) GRPCStatus() *status.Status { panic("GRPCStatus broke") }

// A handler that returns an error whose methods panic does not crash a
// framework-built server: the client gets codes.Internal, unary and
// stream, and the server goes on serving. grpc-go reads the status of the
// error the chain hands it outside any recovery, so the chain must never
// hand it the handler's own error.
func TestServer_ReturnedErrorWithPanickingMethodsDoesNotCrash(t *testing.T) {
	hostile.Isolated(t, func() {
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
		s := NewServer(WithListener(lis), WithLogger(quiet), WithEnvironment("testing"))
		s.MarkAuthConfigured()
		s.RegisterService(func(g interface{}) {
			g.(*grpcgo.Server).RegisterService(&chainDesc, &chainServer{outcome: "poisoned error"})
		})
		if err := s.StartAsync(); err != nil {
			t.Fatalf("StartAsync: %v", err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.Shutdown(ctx)
		})
		conn, err := grpcgo.NewClient(lis.Addr().String(), grpcgo.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatalf("client: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })

		ctx, cancel := context.WithTimeout(context.Background(), hostile.Deadline)
		defer cancel()
		for i := range 2 {
			if err := conn.Invoke(ctx, chainDoMethod, &emptypb.Empty{}, &emptypb.Empty{}); status.Code(err) != codes.Internal {
				t.Fatalf("unary call %d = %v, want Internal", i, err)
			}
			st, err := conn.NewStream(ctx, &chainDesc.Streams[0], chainWatchMethod)
			if err != nil {
				t.Fatalf("NewStream: %v", err)
			}
			if err := st.SendMsg(&emptypb.Empty{}); err != nil {
				t.Fatalf("SendMsg: %v", err)
			}
			_ = st.CloseSend()
			if err := st.RecvMsg(&emptypb.Empty{}); status.Code(err) != codes.Internal {
				t.Fatalf("stream call %d = %v, want Internal", i, err)
			}
		}
	})
}
