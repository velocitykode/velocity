package notification

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// identifiedNotifiable is a notifiable that names itself.
type identifiedNotifiable struct {
	testNotifiable
	identify func() string
}

func (n *identifiedNotifiable) NotifiableID() string { return n.identify() }

// eventRecorder records the events a manager dispatches.
type eventRecorder struct {
	mu     sync.Mutex
	events []any
}

func (r *eventRecorder) dispatch(_ context.Context, event any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *eventRecorder) all() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]any(nil), r.events...)
}

// NotificationSent and NotificationFailed carry what identifies the
// delivery: the notification's and the notifiable's Go types, the send's
// notification ID, the notifiable's own ID (NotifiableIdentifier), the
// channel and the duration; the failure adds its error.
func TestNotificationEvents_CarryTheDeliverysIdentity(t *testing.T) {
	mgr := NewManager()
	mgr.SetChannel("ok", &testChannel{})
	mgr.SetChannel("broken", &testChannel{err: errors.New("delivery failed")})
	rec := &eventRecorder{}
	mgr.SetEventDispatcher(rec.dispatch)

	notifiable := &identifiedNotifiable{identify: func() string { return "user-7" }}
	n := &testNotification{channels: []string{"ok", "broken"}}
	ctx := WithNotificationID(context.Background(), "notif-1")
	_ = mgr.Send(ctx, notifiable, n)

	events := rec.all()
	if len(events) != 2 {
		t.Fatalf("dispatched %d events, want 2: %v", len(events), events)
	}
	sent, ok := events[0].(*NotificationSent)
	if !ok {
		t.Fatalf("first event is %T, want *NotificationSent", events[0])
	}
	failed, ok := events[1].(*NotificationFailed)
	if !ok {
		t.Fatalf("second event is %T, want *NotificationFailed", events[1])
	}
	for _, got := range []struct {
		name                                                                    string
		notificationType, notificationID, notifiableType, notifiableID, channel string
	}{
		{"sent", sent.NotificationType, sent.NotificationID, sent.NotifiableType, sent.NotifiableID, sent.Channel},
		{"failed", failed.NotificationType, failed.NotificationID, failed.NotifiableType, failed.NotifiableID, failed.Channel},
	} {
		if got.notificationType != "*notification.testNotification" {
			t.Errorf("%s: NotificationType = %q", got.name, got.notificationType)
		}
		if got.notificationID != "notif-1" {
			t.Errorf("%s: NotificationID = %q, want the send's ID", got.name, got.notificationID)
		}
		if got.notifiableType != "*notification.identifiedNotifiable" {
			t.Errorf("%s: NotifiableType = %q", got.name, got.notifiableType)
		}
		if got.notifiableID != "user-7" {
			t.Errorf("%s: NotifiableID = %q, want the notifiable's own ID", got.name, got.notifiableID)
		}
	}
	if sent.Channel != "ok" || failed.Channel != "broken" {
		t.Errorf("channels = %q, %q", sent.Channel, failed.Channel)
	}
	if failed.Err == nil || failed.Err.Error() != "delivery failed" {
		t.Errorf("NotificationFailed.Err = %v", failed.Err)
	}
}

// A notifiable without NotifiableIdentifier, or a nil one, leaves the
// identity it cannot give empty.
func TestNotificationEvents_EmptyWhenUnavailable(t *testing.T) {
	mgr := NewManager()
	mgr.SetChannel("ok", &testChannel{})
	rec := &eventRecorder{}
	mgr.SetEventDispatcher(rec.dispatch)

	for _, notifiable := range []any{&testNotifiable{}, nil} {
		_ = mgr.Send(context.Background(), notifiable, &testNotification{channels: []string{"ok"}})
	}
	events := rec.all()
	if len(events) != 2 {
		t.Fatalf("dispatched %d events, want 2", len(events))
	}
	plain, nilOne := events[0].(*NotificationSent), events[1].(*NotificationSent)
	if plain.NotifiableID != "" || plain.NotifiableType != "*notification.testNotifiable" {
		t.Errorf("plain notifiable: ID %q, type %q", plain.NotifiableID, plain.NotifiableType)
	}
	if nilOne.NotifiableID != "" || nilOne.NotifiableType != "" {
		t.Errorf("nil notifiable: ID %q, type %q, want both empty", nilOne.NotifiableID, nilOne.NotifiableType)
	}
	if plain.NotificationID == "" {
		t.Error("a send without a caller ID carries the manager's own")
	}
}

// Both events decode back from their JSON form into the event type, every
// field intact, as a queued listener in another process receives them.
func TestNotificationEvents_HydrateFromJSON(t *testing.T) {
	mgr := NewManager()
	mgr.SetChannel("ok", &testChannel{})
	mgr.SetChannel("broken", &testChannel{err: errors.New("delivery failed")})
	rec := &eventRecorder{}
	mgr.SetEventDispatcher(rec.dispatch)
	notifiable := &identifiedNotifiable{identify: func() string { return "user-7" }}
	_ = mgr.Send(context.Background(), notifiable, &testNotification{channels: []string{"ok", "broken"}})
	events := rec.all()
	if len(events) != 2 {
		t.Fatalf("dispatched %d events, want 2", len(events))
	}

	raw, err := json.Marshal(events[0])
	if err != nil {
		t.Fatalf("marshal NotificationSent: %v", err)
	}
	var sent NotificationSent
	if err := json.Unmarshal(raw, &sent); err != nil {
		t.Fatalf("hydrate NotificationSent from %s: %v", raw, err)
	}
	want := events[0].(*NotificationSent)
	if sent.NotificationType != want.NotificationType || sent.NotificationID != want.NotificationID ||
		sent.NotifiableType != want.NotifiableType || sent.NotifiableID != want.NotifiableID ||
		sent.Channel != want.Channel || sent.Duration != want.Duration {
		t.Errorf("hydrated NotificationSent = %+v, want %+v", sent, want)
	}

	raw, err = json.Marshal(events[1])
	if err != nil {
		t.Fatalf("marshal NotificationFailed: %v", err)
	}
	var failed NotificationFailed
	if err := json.Unmarshal(raw, &failed); err != nil {
		t.Fatalf("hydrate NotificationFailed from %s: %v", raw, err)
	}
	wantF := events[1].(*NotificationFailed)
	if failed.NotifiableID != wantF.NotifiableID || failed.Channel != wantF.Channel ||
		failed.Err == nil || failed.Err.Error() != wantF.Err.Error() {
		t.Errorf("hydrated NotificationFailed = %+v, want %+v", failed, wantF)
	}
}

// NotifiableID is user code: one that panics leaves NotifiableID empty and
// the delivery and its event go on; one that blocks or calls back into the
// manager holds no manager lock while it runs.
func TestNotificationEvents_HostileNotifiableID(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			mgr := NewManager()
			mgr.SetChannel("ok", &testChannel{})
			rec := &eventRecorder{}
			mgr.SetEventDispatcher(rec.dispatch)
			code := hostile.New(t, mode, func() {
				_, _ = mgr.Channel("ok")
				mgr.SetChannel("other", &testChannel{})
			})
			notifiable := &identifiedNotifiable{identify: func() string { code.Run(); return "user-7" }}

			done := make(chan error, 1)
			go func() {
				done <- mgr.Send(context.Background(), notifiable, &testNotification{channels: []string{"ok"}})
			}()
			code.AwaitEntered(t)
			hostile.Within(t, hostile.Deadline, func() {
				_, _ = mgr.Channel("ok")
				mgr.SetChannel("while", &testChannel{})
			})
			code.Release()
			var err error
			hostile.Within(t, hostile.Deadline, func() { err = <-done })
			if err != nil {
				t.Fatalf("Send = %v, want the delivery to succeed", err)
			}
			events := rec.all()
			if len(events) != 1 {
				t.Fatalf("dispatched %d events, want 1", len(events))
			}
			sent := events[0].(*NotificationSent)
			wantID := "user-7"
			if mode == hostile.Panic {
				wantID = ""
			}
			if sent.NotifiableID != wantID {
				t.Errorf("NotifiableID = %q, want %q", sent.NotifiableID, wantID)
			}
		})
	}
}
