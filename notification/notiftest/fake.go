// Package notiftest provides test doubles for the notification subsystem.
//
// The primary double is FakeChannel, a contract.NotificationChannel that
// records every notification it is asked to deliver instead of performing
// real transport. Register it with a notification manager under a channel
// name and route notifications through the manager as usual; the fake then
// exposes assertion helpers describing what was sent.
package notiftest

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// sentNotification records a single Send call.
type sentNotification struct {
	notifiable   interface{}
	notification contract.Notification
}

// FakeChannel is a fake notification channel for testing. It records every
// notification passed to Send rather than delivering it, and provides
// assertion helpers over the recorded calls.
//
// To exercise notification routing, register a FakeChannel with a manager
// under the channel name(s) a notification's Via reports (manager.SetChannel)
// and send through the manager. Production channel registration is untouched.
//
// FakeChannel is safe for concurrent use.
type FakeChannel struct {
	mu   sync.Mutex
	sent []sentNotification
}

// NewFakeChannel creates a new FakeChannel.
func NewFakeChannel() *FakeChannel {
	return &FakeChannel{}
}

// Compile-time check that FakeChannel satisfies the channel contract.
var _ contract.NotificationChannel = (*FakeChannel)(nil)

// Send records the notification instead of delivering it. It always returns nil.
func (f *FakeChannel) Send(ctx context.Context, notifiable interface{}, notification contract.Notification) error {
	f.mu.Lock()
	f.sent = append(f.sent, sentNotification{notifiable: notifiable, notification: notification})
	f.mu.Unlock()
	return nil
}

// AssertSentTo asserts that a notification matching match was sent to the
// given notifiable.
//
// Notifiable equality uses reflect.DeepEqual: a recorded call matches when its
// notifiable is deeply equal to the one passed here. This means notifiables
// are compared by value/structure rather than identity, so two distinct
// pointers to structurally identical values are treated as the same recipient.
// match is invoked only on calls whose notifiable matches; a nil match treats
// any such call as a match.
func (f *FakeChannel) AssertSentTo(t *testing.T, notifiable interface{}, match func(contract.Notification) bool) {
	t.Helper()
	f.mu.Lock()
	sent := append([]sentNotification(nil), f.sent...)
	f.mu.Unlock()

	for _, s := range sent {
		if reflect.DeepEqual(s.notifiable, notifiable) {
			if match == nil || match(s.notification) {
				return
			}
		}
	}
	t.Errorf("expected a matching notification sent to %#v, but none was", notifiable)
}

// AssertNotSentTo asserts that no notification matching match was sent to the
// given notifiable.
//
// Notifiable equality uses reflect.DeepEqual (see AssertSentTo). A nil match
// treats any call to the notifiable as a match, asserting nothing was sent to it.
func (f *FakeChannel) AssertNotSentTo(t *testing.T, notifiable interface{}, match func(contract.Notification) bool) {
	t.Helper()
	f.mu.Lock()
	sent := append([]sentNotification(nil), f.sent...)
	f.mu.Unlock()

	for _, s := range sent {
		if reflect.DeepEqual(s.notifiable, notifiable) {
			if match == nil || match(s.notification) {
				t.Errorf("expected no matching notification sent to %#v, but one was", notifiable)
				return
			}
		}
	}
}

// AssertNothingSent asserts that no notifications were sent at all.
func (f *FakeChannel) AssertNothingSent(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.sent) > 0 {
		t.Errorf("expected no notifications sent, but %d were", len(f.sent))
	}
}

// GetSent returns a copy of the notifications recorded so far, in send order.
func (f *FakeChannel) GetSent() []contract.Notification {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]contract.Notification, len(f.sent))
	for i, s := range f.sent {
		out[i] = s.notification
	}
	return out
}
