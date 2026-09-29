package grpc

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/log"
)

// claimsValidator accepts every token as user 42 of team 7.
type claimsValidator struct{}

func (claimsValidator) ValidateToken(context.Context, string) (interceptors.Claims, error) {
	return &interceptors.BasicClaims{UserID: 42, TeamID: 7}, nil
}

// fieldLines records the fields of every line, the pairs With bound
// included.
type fieldLines struct {
	mu    sync.Mutex
	lines []map[string]any
}

func (l *fieldLines) record(kvs []any) {
	line := map[string]any{}
	for i := 0; i+1 < len(kvs); i += 2 {
		if k, ok := kvs[i].(string); ok {
			line[k] = kvs[i+1]
		}
	}
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
}

func (l *fieldLines) Debug(_ string, kvs ...any)      { l.record(kvs) }
func (l *fieldLines) Info(_ string, kvs ...any)       { l.record(kvs) }
func (l *fieldLines) Warn(_ string, kvs ...any)       { l.record(kvs) }
func (l *fieldLines) Error(_ string, kvs ...any)      { l.record(kvs) }
func (l *fieldLines) Fatal(_ string, kvs ...any)      { l.record(kvs) }
func (l *fieldLines) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }

// A framework-built server whose app adds Auth (a user interceptor, so it
// runs after the default call lifecycle interceptor) keeps the caller's
// user and team on the request line and on the failed and completed
// events: user fields come from the context the handler received.
func TestServerChain_UserFieldsFromAuthReachTheLineAndEvents(t *testing.T) {
	for _, kind := range []string{"unary", "stream"} {
		t.Run(kind, func(t *testing.T) {
			lines := &fieldLines{}
			evs := &grpcEventLog{}
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
			s := NewServer(WithListener(lis), WithLogger(quiet), WithEnvironment("testing"), WithCallOptions(
				interceptors.WithRequestLine(),
				interceptors.WithLogger(lines),
				interceptors.WithEventDispatcher(evs.dispatch),
				interceptors.WithExtraFields(func(ctx context.Context) []any {
					if c := interceptors.ClaimsFromContext(ctx); c != nil {
						return []any{"extra_user", c.GetUserID()}
					}
					return nil
				}),
			))
			s.UseAll(interceptors.Auth(claimsValidator{}))
			s.RegisterService(func(g interface{}) {
				g.(*grpcgo.Server).RegisterService(&chainDesc, &chainServer{outcome: "error"})
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

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer token")
			var callErr error
			if kind == "unary" {
				callErr = conn.Invoke(ctx, chainDoMethod, &emptypb.Empty{}, &emptypb.Empty{})
			} else {
				st, err := conn.NewStream(ctx, &chainDesc.Streams[0], chainWatchMethod)
				if err != nil {
					t.Fatalf("NewStream: %v", err)
				}
				_ = st.SendMsg(&emptypb.Empty{})
				_ = st.CloseSend()
				callErr = st.RecvMsg(&emptypb.Empty{})
			}
			if status.Code(callErr) != codes.Internal {
				t.Fatalf("code = %v (%v), want the handler's Internal", status.Code(callErr), callErr)
			}

			lines.mu.Lock()
			var line map[string]any
			for _, l := range lines.lines {
				if l["code"] != nil {
					line = l
				}
			}
			lines.mu.Unlock()
			if line == nil {
				t.Fatal("no request line")
			}
			if line["user_id"] != uint(42) || line["team_id"] != uint(7) || line["extra_user"] != uint(42) {
				t.Errorf("request line user_id %v team_id %v extra_user %v, want 42 7 42", line["user_id"], line["team_id"], line["extra_user"])
			}
			var ends int
			evs.mu.Lock()
			for _, ev := range evs.events {
				var user, team uint
				switch e := ev.(type) {
				case *grpcevents.RequestFailed:
					user, team = e.UserID, e.TeamID
				case *grpcevents.RequestCompleted:
					user, team = e.UserID, e.TeamID
				case *grpcevents.StreamFailed:
					user, team = e.UserID, e.TeamID
				case *grpcevents.StreamCompleted:
					user, team = e.UserID, e.TeamID
				default:
					continue
				}
				ends++
				if user != 42 || team != 7 {
					t.Errorf("%T UserID %d TeamID %d, want 42 7", ev, user, team)
				}
			}
			evs.mu.Unlock()
			if ends != 2 {
				t.Errorf("failed and completed events = %d, want 2", ends)
			}
		})
	}
}

// countingLines counts request lines (lines that carry a status code).
type countingLines struct {
	mu sync.Mutex
	n  int
}

func (l *countingLines) record(kvs []any) {
	for i := 0; i+1 < len(kvs); i += 2 {
		if kvs[i] == "code" {
			l.mu.Lock()
			l.n++
			l.mu.Unlock()
		}
	}
}

func (l *countingLines) Debug(_ string, kvs ...any)      { l.record(kvs) }
func (l *countingLines) Info(_ string, kvs ...any)       { l.record(kvs) }
func (l *countingLines) Warn(_ string, kvs ...any)       { l.record(kvs) }
func (l *countingLines) Error(_ string, kvs ...any)      { l.record(kvs) }
func (l *countingLines) Fatal(_ string, kvs ...any)      { l.record(kvs) }
func (l *countingLines) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }

func (l *countingLines) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n
}

// A CallLifecycle an app adds to a framework-built server, which already
// installs one by default, does not become a second owner of the call: a
// panicking call still gets exactly one request line, one failed and
// completed pair and one report across both configurations.
func TestServerChain_NestedCallLifecycleKeepsOneOwner(t *testing.T) {
	for _, kind := range []string{"unary", "stream"} {
		t.Run(kind, func(t *testing.T) {
			lines, userLines := &countingLines{}, &countingLines{}
			evs, userEvs := &grpcEventLog{}, &grpcEventLog{}
			reports, userReports := &chainReports{}, &chainReports{}
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
			s := NewServer(WithListener(lis), WithLogger(quiet), WithEnvironment("testing"), WithReporter(reports), WithCallOptions(
				interceptors.WithRequestLine(), interceptors.WithLogger(lines), interceptors.WithEventDispatcher(evs.dispatch),
			))
			s.UseAll(interceptors.CallLifecycle(
				interceptors.WithRequestLine(), interceptors.WithLogger(userLines),
				interceptors.WithEventDispatcher(userEvs.dispatch), interceptors.WithReporter(userReports),
			))
			s.MarkAuthConfigured()
			s.RegisterService(func(g interface{}) {
				g.(*grpcgo.Server).RegisterService(&chainDesc, &chainServer{outcome: "panic"})
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
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var callErr error
			if kind == "unary" {
				callErr = conn.Invoke(ctx, chainDoMethod, &emptypb.Empty{}, &emptypb.Empty{})
			} else {
				st, err := conn.NewStream(ctx, &chainDesc.Streams[0], chainWatchMethod)
				if err != nil {
					t.Fatalf("NewStream: %v", err)
				}
				_ = st.SendMsg(&emptypb.Empty{})
				_ = st.CloseSend()
				callErr = st.RecvMsg(&emptypb.Empty{})
			}
			if status.Code(callErr) != codes.Internal {
				t.Fatalf("code = %v (%v), want Internal", status.Code(callErr), callErr)
			}

			if n := lines.count() + userLines.count(); n != 1 {
				t.Errorf("request lines = %d (default %d, nested %d), want 1", n, lines.count(), userLines.count())
			}
			var ends int
			for _, log := range []*grpcEventLog{evs, userEvs} {
				log.mu.Lock()
				for _, ev := range log.events {
					switch ev.(type) {
					case *grpcevents.RequestFailed, *grpcevents.RequestCompleted, *grpcevents.StreamFailed, *grpcevents.StreamCompleted:
						ends++
					}
				}
				log.mu.Unlock()
			}
			if ends != 2 {
				t.Errorf("failed and completed events = %d, want one pair", ends)
			}
			a, _ := reports.all()
			b, _ := userReports.all()
			if len(a)+len(b) != 1 {
				t.Errorf("reports = %d (default %d, nested %d), want 1", len(a)+len(b), len(a), len(b))
			}
		})
	}
}
