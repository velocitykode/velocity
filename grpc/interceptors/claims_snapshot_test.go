package interceptors_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/grpc/interceptors"
)

// panickingClaims is an application Claims whose getters panic.
type panickingClaims struct{}

func (panickingClaims) GetUserID() uint { panic("user id getter broke") }
func (panickingClaims) GetTeamID() uint { panic("team id getter broke") }

// countingClaims counts its getter calls.
type countingClaims struct {
	user, team uint
	calls      *atomic.Int32
}

func (c countingClaims) GetUserID() uint { c.calls.Add(1); return c.user }
func (c countingClaims) GetTeamID() uint { c.calls.Add(1); return c.team }

// withClaims is a middle interceptor that runs the rest of the chain under
// claims, as Auth does.
func withClaims(claims interceptors.Claims) func(next func(context.Context) error, ctx context.Context) error {
	return func(next func(context.Context) error, ctx context.Context) error {
		return next(interceptors.ContextWithClaims(ctx, claims))
	}
}

// A claims getter is application code: one that panics (or a typed-nil
// *BasicClaims) is contained when the call ends. The call keeps its
// terminal sequence and its report, the user fields are left empty, and
// the process survives, unary and stream, with and without the request
// line.
func TestCallLifecycle_PanickingClaimsNeverSkipTheEnd(t *testing.T) {
	hostile := map[string]interceptors.Claims{
		"panicking getter": panickingClaims{},
		"typed nil":        (*interceptors.BasicClaims)(nil),
	}
	for name, claims := range hostile {
		for _, kind := range []string{"unary", "stream"} {
			for _, line := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/line=%v", name, kind, line), func(t *testing.T) {
					reports := &layerReports{}
					evs := &eventCollector{}
					lines := newBoundLogger()
					opts := []interceptors.CallOption{interceptors.WithReporter(reports), interceptors.WithStackTrace(false),
						interceptors.WithEventDispatcher(evs.dispatch), interceptors.WithLogger(lines)}
					if line {
						opts = append(opts, interceptors.WithRequestLine())
					}
					calls := interceptors.CallLifecycle(opts...)
					var err error
					func() {
						defer func() {
							if p := recover(); p != nil {
								t.Fatalf("a claims getter's panic escaped the call: %v", p)
							}
						}()
						err = runChain(kind, calls, withClaims(claims), func(context.Context) error { return status.Error(codes.Internal, "broke") })
					}()
					if status.Code(err) != codes.Internal {
						t.Fatalf("code = %v, want Internal", status.Code(err))
					}
					var kinds []string
					for _, ev := range evs.snapshot() {
						switch v := ev.(type) {
						case *grpcevents.RequestStarted, *grpcevents.StreamStarted:
							kinds = append(kinds, "started")
						case *grpcevents.RequestFailed:
							kinds = append(kinds, "failed")
							if v.UserID != 0 || v.TeamID != 0 {
								t.Errorf("failed event UserID %d TeamID %d, want empty", v.UserID, v.TeamID)
							}
						case *grpcevents.StreamFailed:
							kinds = append(kinds, "failed")
						case *grpcevents.RequestCompleted, *grpcevents.StreamCompleted:
							kinds = append(kinds, "completed")
						}
					}
					if !equalKinds(kinds, []string{"started", "failed", "completed"}) {
						t.Errorf("lifecycle events = %v, want one terminal sequence", kinds)
					}
					if reports.count() != 1 {
						t.Errorf("reports = %d, want 1", reports.count())
					} else if ec := reports.contexts()[0]; ec.UserID != "" || ec.Extra["team_id"] != nil {
						t.Errorf("report UserID %q team_id %v, want empty", ec.UserID, ec.Extra["team_id"])
					}
					if line {
						if l := lines.last(t); l["code"] != "Internal" || l["user_id"] != nil || l["team_id"] != nil {
							t.Errorf("request line %v, want the Internal line without user fields", l)
						}
					}
				})
			}
		}
	}
}

// The call's error report carries the identity of the call's claims, from
// the same snapshot as the request line and the events: the user id as
// ErrorContext.UserID and the team id as Extra "team_id", for a returned
// internal error and for a recovered panic. Zero means none.
func TestCallLifecycle_ReportCarriesTheCallIdentity(t *testing.T) {
	ends := map[string]func(context.Context) error{
		"internal error": func(context.Context) error { return status.Error(codes.Internal, "broke") },
		"panic":          func(context.Context) error { panic("broke") },
	}
	for name, handler := range ends {
		for _, kind := range []string{"unary", "stream"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				reports := &layerReports{}
				evs := &eventCollector{}
				lines := newBoundLogger()
				calls := interceptors.CallLifecycle(interceptors.WithReporter(reports), interceptors.WithStackTrace(false),
					interceptors.WithEventDispatcher(evs.dispatch), interceptors.WithLogger(lines), interceptors.WithRequestLine())
				_ = runChain(kind, calls, withClaims(&interceptors.BasicClaims{UserID: 42, TeamID: 7}), handler)
				if reports.count() != 1 {
					t.Fatalf("reports = %d, want 1", reports.count())
				}
				if ec := reports.contexts()[0]; ec.UserID != "42" || ec.Extra["team_id"] != uint(7) {
					t.Errorf("report UserID %q team_id %v, want 42 and 7", ec.UserID, ec.Extra["team_id"])
				}
				if l := lines.last(t); l["user_id"] != uint(42) || l["team_id"] != uint(7) {
					t.Errorf("request line user_id %v team_id %v, want 42 7", l["user_id"], l["team_id"])
				}
				for _, ev := range evs.snapshot() {
					if c, ok := ev.(*grpcevents.RequestCompleted); ok && (c.UserID != 42 || c.TeamID != 7) {
						t.Errorf("completed UserID %d TeamID %d, want 42 7", c.UserID, c.TeamID)
					}
				}
			})
		}
	}

	t.Run("no claims", func(t *testing.T) {
		reports := &layerReports{}
		calls := interceptors.CallLifecycle(interceptors.WithReporter(reports), interceptors.WithStackTrace(false))
		_ = runChain("unary", calls, withClaims(&interceptors.BasicClaims{}), ends["internal error"])
		if ec := reports.contexts()[0]; ec.UserID != "" || ec.Extra["team_id"] != nil {
			t.Errorf("report UserID %q team_id %v, want none for zero ids", ec.UserID, ec.Extra["team_id"])
		}
	})
}

// The claims are read once per call: the request line, the events and the
// report share one snapshot, so each getter runs once.
func TestCallLifecycle_ClaimsAreReadOncePerCall(t *testing.T) {
	for _, kind := range []string{"unary", "stream"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			pair := interceptors.CallLifecycle(interceptors.WithReporter(&layerReports{}), interceptors.WithStackTrace(false),
				interceptors.WithEventDispatcher((&eventCollector{}).dispatch), interceptors.WithLogger(newBoundLogger()), interceptors.WithRequestLine())
			_ = runChain(kind, pair, withClaims(countingClaims{user: 1, team: 2, calls: &calls}),
				func(context.Context) error { return status.Error(codes.Internal, "broke") })
			if got := calls.Load(); got != 2 {
				t.Errorf("getter calls = %d, want 2 (each getter once)", got)
			}
		})
	}
}

// A late panic racing the owner's end shares the call's one snapshot:
// under -race there is no data race, and each report of a call carries
// the identity its completed event carries (the claims are published by
// the handler's layer, so which one wins depends on the race, but a call
// never mixes two).
func TestCallLifecycle_LatePanicSharesTheClaimsSnapshot(t *testing.T) {
	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reports := &layerReports{}
			evs := &eventCollector{}
			calls := interceptors.CallLifecycle(interceptors.WithReporter(reports), interceptors.WithStackTrace(false),
				interceptors.WithEventDispatcher(evs.dispatch))
			release, finished := make(chan struct{}), make(chan struct{})
			close(release)
			middle := timeoutMiddle(codes.Internal, release, finished)
			claimed := func(next func(context.Context) error, ctx context.Context) error {
				return middle(next, interceptors.ContextWithClaims(ctx, &interceptors.BasicClaims{UserID: 42, TeamID: 7}))
			}
			_ = runChain([]string{"unary", "stream"}[i%2], calls, claimed, func(context.Context) error { panic("late") })
			<-finished
			var user uint
			for _, ev := range evs.snapshot() {
				switch c := ev.(type) {
				case *grpcevents.RequestCompleted:
					user = c.UserID
				case *grpcevents.StreamCompleted:
					user = c.UserID
				}
			}
			want := ""
			if user != 0 {
				want = "42"
			}
			for _, ec := range reports.contexts() {
				if ec.UserID != want {
					t.Errorf("report UserID %q, want %q as the completed event carries", ec.UserID, want)
				}
			}
		}(i)
	}
	wg.Wait()
}
