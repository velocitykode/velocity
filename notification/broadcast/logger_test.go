package broadcast

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	logdrivers "github.com/velocitykode/velocity/log/drivers"
	"github.com/velocitykode/velocity/notification"
	"github.com/velocitykode/velocity/trace"
)

var _ contract.LoggerAware = (*BroadcastChannel)(nil)

const (
	noAuthorizerWarning = "velocity/notification: broadcast channel has no BroadcastChannelAuthorizer installed"
	rejectedWarning     = "velocity/notification: broadcast authorizer rejected channels"
)

// The channel's two warnings go through its logger, and nothing through
// the standard library log or slog.Default.
func TestBroadcastChannel_WarningsGoThroughItsLogger(t *testing.T) {
	stdlib := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)
	out := &fallbacklogtest.Output{}
	ch, _ := newWiredBroadcastChannel(t)
	ch.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
	notifiable := &tenantNotifiable{tenantID: "A", userID: "1"}

	open := &multiChannelNotification{channels: []string{"private-anything-goes"}}
	for i := 0; i < 2; i++ {
		if err := ch.Send(context.Background(), notifiable, open); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	ch.SetAuthorizer(BroadcastChannelAuthorizerFunc(tenantPrefixAuthorizer))
	mixed := &multiChannelNotification{channels: []string{"private-tenant-A-user-1", "private-tenant-B-user-2"}}
	if err := ch.Send(context.Background(), notifiable, mixed); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := strings.Count(out.String(), "WARN: "+noAuthorizerWarning); got != 1 {
		t.Errorf("no-authorizer warn lines = %d, want 1 (%q)", got, out.String())
	}
	if got := strings.Count(out.String(), "WARN: "+rejectedWarning); got != 1 {
		t.Errorf("rejected warn lines = %d, want 1 (%q)", got, out.String())
	}
	if !strings.Contains(out.String(), "private-tenant-B-user-2") {
		t.Errorf("rejected warning does not name the denied channel: %q", out.String())
	}
	if s := stdlib.String() + fallback.String(); s != "" {
		t.Errorf("stdlib / slog.Default / fallback got %q, want nothing", s)
	}
}

// Without a logger the warning goes through the fallback logger.
func TestBroadcastChannel_WithoutLoggerWarnsThroughTheFallback(t *testing.T) {
	stdlib := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)
	ch, _ := newWiredBroadcastChannel(t)
	notifiable := &tenantNotifiable{tenantID: "A", userID: "1"}
	if err := ch.Send(context.Background(), notifiable, &multiChannelNotification{channels: []string{"private-x"}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := fallback.Count("WARN", noAuthorizerWarning); got != 1 {
		t.Errorf("fallback warn lines = %d, want 1 (%q)", got, fallback.String())
	}
	if s := stdlib.String(); s != "" {
		t.Errorf("stdlib / slog.Default got %q, want nothing", s)
	}
}

// The notification manager hands its logger to a broadcast channel set
// before or after SetLogger.
func TestManager_HandsItsLoggerToTheBroadcastChannel(t *testing.T) {
	for _, order := range []string{"channel first", "logger first"} {
		t.Run(order, func(t *testing.T) {
			fallback := fallbacklogtest.Capture(t)
			out := &fallbacklogtest.Output{}
			l := logdrivers.NewConsoleLoggerTo(out, 0)
			ch, _ := newWiredBroadcastChannel(t)
			m := notification.NewManager()
			if order == "channel first" {
				m.SetChannel("broadcast", ch)
				m.SetLogger(l)
			} else {
				m.SetLogger(l)
				m.SetChannel("broadcast", ch)
			}
			notifiable := &tenantNotifiable{tenantID: "A", userID: "1"}
			if err := m.Send(context.Background(), notifiable, &multiChannelNotification{channels: []string{"private-x"}}); err != nil {
				t.Fatalf("Send: %v", err)
			}
			if got := strings.Count(out.String(), "WARN: "+noAuthorizerWarning); got != 1 {
				t.Errorf("manager logger warn lines = %d, want 1 (%q)", got, out.String())
			}
			if s := fallback.String(); s != "" {
				t.Errorf("fallback got %q, want nothing", s)
			}
		})
	}
}

// SetLogger may run while notifications are sent: the logger is held under
// the channel's read-write mutex.
func TestBroadcastChannel_SetLoggerWhileSendingIsSafe(t *testing.T) {
	fallbacklogtest.Capture(t)
	out := &fallbacklogtest.Output{}
	ch, _ := newWiredBroadcastChannel(t)
	ch.SetAuthorizer(BroadcastChannelAuthorizerFunc(tenantPrefixAuthorizer))
	notifiable := &tenantNotifiable{tenantID: "A", userID: "1"}
	mixed := &multiChannelNotification{channels: []string{"private-tenant-A-user-1", "private-tenant-B-user-2"}}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				ch.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
				ch.SetLogger(nil)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = ch.Send(context.Background(), notifiable, mixed)
			}
		}()
	}
	wg.Wait()
}

// Both warnings carry the request, trace and span ids of the ctx the
// notification was sent under.
func TestBroadcastChannel_WarningsCarryTheRequestIDs(t *testing.T) {
	out := &fallbacklogtest.Output{}
	ch, _ := newWiredBroadcastChannel(t)
	ch.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
	ctx := trace.WithTrace(trace.WithRequestID(context.Background(), "req-bc-1"), "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7")
	notifiable := &tenantNotifiable{tenantID: "A", userID: "1"}
	if err := ch.Send(ctx, notifiable, &multiChannelNotification{channels: []string{"private-anything-goes"}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	ch.SetAuthorizer(BroadcastChannelAuthorizerFunc(tenantPrefixAuthorizer))
	if err := ch.Send(ctx, notifiable, &multiChannelNotification{channels: []string{"private-tenant-A-user-1", "private-tenant-B-user-2"}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	want := "request_id=req-bc-1 trace_id=4bf92f3577b34da6a3ce929d0e0e4736 span_id=00f067aa0ba902b7"
	lines := out.Lines()
	if len(lines) != 2 {
		t.Fatalf("lines = %q, want the two warnings", lines)
	}
	for _, l := range lines {
		if !strings.Contains(l, want) {
			t.Errorf("line %q does not carry %q", l, want)
		}
	}
}

// SetLogger's edge inputs: nil puts the channel back on the fallback; a
// zero-value channel takes a logger and warns through it.
func TestBroadcastChannel_SetLoggerEdgeInputs(t *testing.T) {
	t.Run("nil restores the fallback", func(t *testing.T) {
		fallback := fallbacklogtest.Capture(t)
		out := &fallbacklogtest.Output{}
		ch, _ := newWiredBroadcastChannel(t)
		ch.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
		ch.SetLogger(nil)
		_ = ch.Send(context.Background(), &tenantNotifiable{tenantID: "A", userID: "1"}, &multiChannelNotification{channels: []string{"private-x"}})
		if out.String() != "" {
			t.Errorf("replaced logger got %q, want nothing", out.String())
		}
		if got := fallback.Count("WARN", noAuthorizerWarning); got != 1 {
			t.Errorf("fallback warn lines = %d, want 1 (%q)", got, fallback.String())
		}
	})

	t.Run("zero value", func(t *testing.T) {
		out := &fallbacklogtest.Output{}
		var ch BroadcastChannel
		ch.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
		if a := ch.authorizerOrWarn(context.Background()); a != nil {
			t.Fatalf("zero value authorizer = %v, want nil", a)
		}
		if got := strings.Count(out.String(), "WARN: "+noAuthorizerWarning); got != 1 {
			t.Errorf("logger warn lines = %d, want 1 (%q)", got, out.String())
		}
	})
}
