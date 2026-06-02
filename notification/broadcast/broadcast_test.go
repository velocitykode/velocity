package broadcast

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	velbroadcast "github.com/velocitykode/velocity/broadcast"
	"github.com/velocitykode/velocity/notification"
)

// captureDriver implements broadcast.Driver and records every Broadcast
// call so tests can assert which channel names actually went over the
// wire after the authorizer ran.
type captureDriver struct {
	mu      sync.Mutex
	calls   []captureCall
	emitErr error
}

type captureCall struct {
	channels []string
	event    string
	data     interface{}
}

func (d *captureDriver) Broadcast(channels []string, event string, data interface{}) error {
	return d.BroadcastCtx(context.Background(), channels, event, data)
}

func (d *captureDriver) BroadcastCtx(_ context.Context, channels []string, event string, data interface{}) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := make([]string, len(channels))
	copy(cp, channels)
	d.calls = append(d.calls, captureCall{channels: cp, event: event, data: data})
	return d.emitErr
}

func (d *captureDriver) BroadcastExcept(channels []string, event string, data interface{}, socketID string) error {
	return d.BroadcastExceptCtx(context.Background(), channels, event, data, socketID)
}

func (d *captureDriver) BroadcastExceptCtx(ctx context.Context, channels []string, event string, data interface{}, _ string) error {
	return d.BroadcastCtx(ctx, channels, event, data)
}

func (d *captureDriver) GetClients(_ string) []string { return nil }

func (d *captureDriver) recorded() []captureCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]captureCall, len(d.calls))
	copy(out, d.calls)
	return out
}

// tenantNotifiable carries a TenantID so the authorizer can compare it
// against the channel-name prefix.
type tenantNotifiable struct {
	tenantID string
	userID   string
}

func (n *tenantNotifiable) NotificationRoute(channel string) string {
	if channel == "broadcast" {
		return "tenant-" + n.tenantID + "-user-" + n.userID
	}
	return ""
}

// multiChannelNotification routes onto two channels; one matches the
// tenant prefix and the other does not, so the authorizer should keep
// the first and reject the second.
type multiChannelNotification struct {
	channels []string
}

func (n *multiChannelNotification) Via(_ interface{}) []string { return []string{"broadcast"} }

func (n *multiChannelNotification) ToBroadcast(_ interface{}) *notification.BroadcastMessage {
	msg := notification.NewBroadcastMessage("tenant.event")
	msg.On(n.channels...)
	msg.Set("payload", "hello")
	return msg
}

type sharedBroadcastNotification struct {
	msg *notification.BroadcastMessage
}

func (n *sharedBroadcastNotification) Via(_ interface{}) []string { return []string{"broadcast"} }

func (n *sharedBroadcastNotification) ToBroadcast(_ interface{}) *notification.BroadcastMessage {
	return n.msg
}

func newWiredBroadcastChannel(t *testing.T) (*BroadcastChannel, *captureDriver) {
	t.Helper()
	drv := &captureDriver{}
	mgr := velbroadcast.New(drv)
	ch := NewBroadcastChannel()
	ch.SetBroadcaster(mgr)
	return ch, drv
}

// tenantPrefixAuthorizer admits a channel only when it starts with the
// notifiable's tenant prefix. Mirrors the typical multi-tenant model
// where every channel name is namespaced by tenant.
func tenantPrefixAuthorizer(notifiable interface{}, channel string) bool {
	tn, ok := notifiable.(*tenantNotifiable)
	if !ok {
		return false
	}
	prefix := "private-tenant-" + tn.tenantID + "-"
	return strings.HasPrefix(channel, prefix)
}

func TestBroadcastChannel_Authorizer_RejectsMismatchedTenant(t *testing.T) {
	ch, drv := newWiredBroadcastChannel(t)
	ch.SetAuthorizer(BroadcastChannelAuthorizerFunc(tenantPrefixAuthorizer))

	notifiable := &tenantNotifiable{tenantID: "A", userID: "1"}
	notification := &multiChannelNotification{
		channels: []string{
			"private-tenant-B-user-1", // foreign tenant -> rejected
		},
	}

	err := ch.Send(context.Background(), notifiable, notification)
	if err == nil {
		t.Fatal("expected error when every channel rejected by authorizer")
	}
	if !strings.Contains(err.Error(), "rejected by authorizer") {
		t.Errorf("error did not mention authorizer rejection: %v", err)
	}
	if calls := drv.recorded(); len(calls) != 0 {
		t.Errorf("broadcast driver should not have been called when all channels denied; got %d calls", len(calls))
	}
}

func TestBroadcastChannel_Authorizer_PassesAllowedTenant(t *testing.T) {
	ch, drv := newWiredBroadcastChannel(t)
	ch.SetAuthorizer(BroadcastChannelAuthorizerFunc(tenantPrefixAuthorizer))

	notifiable := &tenantNotifiable{tenantID: "A", userID: "1"}
	notification := &multiChannelNotification{
		channels: []string{"private-tenant-A-user-1"},
	}

	if err := ch.Send(context.Background(), notifiable, notification); err != nil {
		t.Fatalf("expected matching tenant channel to be delivered: %v", err)
	}
	calls := drv.recorded()
	if len(calls) != 1 {
		t.Fatalf("expected 1 broadcast, got %d", len(calls))
	}
	if len(calls[0].channels) != 1 || calls[0].channels[0] != "private-tenant-A-user-1" {
		t.Errorf("unexpected channels delivered: %v", calls[0].channels)
	}
}

func TestBroadcastChannel_Authorizer_FiltersMixedChannels(t *testing.T) {
	ch, drv := newWiredBroadcastChannel(t)
	ch.SetAuthorizer(BroadcastChannelAuthorizerFunc(tenantPrefixAuthorizer))

	notifiable := &tenantNotifiable{tenantID: "A", userID: "1"}
	notification := &multiChannelNotification{
		channels: []string{
			"private-tenant-A-user-1", // allowed
			"private-tenant-B-user-2", // denied
			"private-tenant-A-user-2", // allowed
		},
	}

	if err := ch.Send(context.Background(), notifiable, notification); err != nil {
		t.Fatalf("expected partial allow to succeed: %v", err)
	}
	calls := drv.recorded()
	if len(calls) != 1 {
		t.Fatalf("expected 1 broadcast, got %d", len(calls))
	}
	got := calls[0].channels
	if len(got) != 2 || got[0] != "private-tenant-A-user-1" || got[1] != "private-tenant-A-user-2" {
		t.Errorf("expected filtered channels [A-1 A-2], got %v", got)
	}
}

func TestBroadcastChannel_Send_DoesNotMutateCallerChannels(t *testing.T) {
	ch, drv := newWiredBroadcastChannel(t)
	ch.SetAuthorizer(BroadcastChannelAuthorizerFunc(func(_ interface{}, channel string) bool {
		return channel != "b"
	}))

	broadcastMsg := notification.NewBroadcastMessage("tenant.event").On("a", "b", "c")
	broadcastMsg.Set("payload", "hello")
	originalChannels := slices.Clone(broadcastMsg.Channels)
	originalLen := len(broadcastMsg.Channels)
	notification := &sharedBroadcastNotification{msg: broadcastMsg}

	if err := ch.Send(context.Background(), &tenantNotifiable{tenantID: "A", userID: "1"}, notification); err != nil {
		t.Fatalf("send: %v", err)
	}

	if len(broadcastMsg.Channels) != originalLen {
		t.Fatalf("broadcast message channels len = %d, want %d", len(broadcastMsg.Channels), originalLen)
	}
	if !slices.Equal(broadcastMsg.Channels, originalChannels) {
		t.Fatalf("broadcast message channels mutated: got %v, want %v", broadcastMsg.Channels, originalChannels)
	}

	calls := drv.recorded()
	if len(calls) != 1 {
		t.Fatalf("expected 1 broadcast, got %d", len(calls))
	}
	if !slices.Equal(calls[0].channels, []string{"a", "c"}) {
		t.Fatalf("emitted channels = %v, want [a c]", calls[0].channels)
	}
}

func TestBroadcastChannel_Send_SharedMessageConcurrent(t *testing.T) {
	ch, drv := newWiredBroadcastChannel(t)
	ch.SetAuthorizer(BroadcastChannelAuthorizerFunc(func(_ interface{}, channel string) bool {
		return channel != "b" && channel != "d"
	}))

	broadcastMsg := notification.NewBroadcastMessage("tenant.event").On("a", "b", "c", "d", "e")
	broadcastMsg.Set("payload", "hello")
	originalChannels := slices.Clone(broadcastMsg.Channels)
	notification := &sharedBroadcastNotification{msg: broadcastMsg}
	notifiable := &tenantNotifiable{tenantID: "A", userID: "1"}

	const sends = 32
	var wg sync.WaitGroup
	errs := make(chan error, sends)
	for i := 0; i < sends; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- ch.Send(context.Background(), notifiable, notification)
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	if !slices.Equal(broadcastMsg.Channels, originalChannels) {
		t.Fatalf("broadcast message channels mutated: got %v, want %v", broadcastMsg.Channels, originalChannels)
	}

	calls := drv.recorded()
	if len(calls) != sends {
		t.Fatalf("expected %d broadcasts, got %d", sends, len(calls))
	}
	wantChannels := []string{"a", "c", "e"}
	for i, call := range calls {
		if !slices.Equal(call.channels, wantChannels) {
			t.Fatalf("call %d emitted channels = %v, want %v", i, call.channels, wantChannels)
		}
	}
}

func TestBroadcastChannel_NoAuthorizer_AllowsAllButWarns(t *testing.T) {
	ch, drv := newWiredBroadcastChannel(t)
	// No SetAuthorizer call: default open-but-warn path must still
	// deliver so single-tenant deployments are not broken.

	notifiable := &tenantNotifiable{tenantID: "A", userID: "1"}
	notification := &multiChannelNotification{
		channels: []string{"private-anything-goes"},
	}

	if err := ch.Send(context.Background(), notifiable, notification); err != nil {
		t.Fatalf("expected default open-but-warn to deliver: %v", err)
	}
	if len(drv.recorded()) != 1 {
		t.Fatalf("expected 1 broadcast, got %d", len(drv.recorded()))
	}

	// Second Send must not re-warn (one-shot warn invariant).
	if err := ch.Send(context.Background(), notifiable, notification); err != nil {
		t.Fatalf("second Send unexpected error: %v", err)
	}
}

func TestBroadcastChannel_Authorizer_NilReverts(t *testing.T) {
	ch, drv := newWiredBroadcastChannel(t)
	ch.SetAuthorizer(BroadcastChannelAuthorizerFunc(func(_ interface{}, _ string) bool { return false }))
	ch.SetAuthorizer(nil) // revert to default open-but-warn

	notifiable := &tenantNotifiable{tenantID: "A", userID: "1"}
	notification := &multiChannelNotification{channels: []string{"private-anywhere"}}

	if err := ch.Send(context.Background(), notifiable, notification); err != nil {
		t.Fatalf("nil-authorizer must revert to default: %v", err)
	}
	if len(drv.recorded()) != 1 {
		t.Fatalf("expected 1 broadcast after revert, got %d", len(drv.recorded()))
	}
}

func TestBroadcastChannel_Authorizer_HookInstalledCorrectly(t *testing.T) {
	ch, _ := newWiredBroadcastChannel(t)
	var sawNotifiable interface{}
	var sawChannel string
	ch.SetAuthorizer(BroadcastChannelAuthorizerFunc(func(n interface{}, c string) bool {
		sawNotifiable = n
		sawChannel = c
		return true
	}))

	notifiable := &tenantNotifiable{tenantID: "A", userID: "42"}
	notification := &multiChannelNotification{channels: []string{"private-tenant-A-user-42"}}
	if err := ch.Send(context.Background(), notifiable, notification); err != nil {
		t.Fatalf("send: %v", err)
	}
	if sawNotifiable != notifiable {
		t.Errorf("authorizer notifiable arg = %v, want %v", sawNotifiable, notifiable)
	}
	if sawChannel != "private-tenant-A-user-42" {
		t.Errorf("authorizer channel arg = %q, want %q", sawChannel, "private-tenant-A-user-42")
	}
}

func TestBroadcastChannel_Authorizer_DriverErrorPropagates(t *testing.T) {
	ch, drv := newWiredBroadcastChannel(t)
	drv.emitErr = errors.New("transport down")
	ch.SetAuthorizer(BroadcastChannelAuthorizerFunc(tenantPrefixAuthorizer))

	notifiable := &tenantNotifiable{tenantID: "A", userID: "1"}
	notification := &multiChannelNotification{channels: []string{"private-tenant-A-user-1"}}

	err := ch.Send(context.Background(), notifiable, notification)
	if err == nil {
		t.Fatal("expected driver error to propagate")
	}
	if !strings.Contains(err.Error(), "transport down") {
		t.Errorf("expected transport error, got %v", err)
	}
}
