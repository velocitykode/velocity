package notiftest

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/notification"
)

// testNotification is a minimal contract.Notification used to exercise the fake.
type testNotification struct {
	channels []string
	tag      string
}

func (n testNotification) Via(notifiable interface{}) []string { return n.channels }

// user is a simple notifiable.
type user struct {
	ID int
}

func TestFakeChannel(t *testing.T) {
	alice := user{ID: 1}
	bob := user{ID: 2}

	tests := []struct {
		name string
		// check runs the scenario against a fresh fake routed through a manager.
		check func(t *testing.T, mgr *notification.Manager, fake *FakeChannel)
	}{
		{
			name: "AssertSentTo matches recipient and notification",
			check: func(t *testing.T, mgr *notification.Manager, fake *FakeChannel) {
				_ = mgr.Send(context.Background(), alice, testNotification{channels: []string{"fake"}, tag: "welcome"})

				fake.AssertSentTo(t, alice, func(n contract.Notification) bool {
					return n.(testNotification).tag == "welcome"
				})
				fake.AssertSentTo(t, alice, nil)
			},
		},
		{
			name: "AssertNotSentTo passes for untouched recipient",
			check: func(t *testing.T, mgr *notification.Manager, fake *FakeChannel) {
				_ = mgr.Send(context.Background(), alice, testNotification{channels: []string{"fake"}, tag: "welcome"})

				fake.AssertNotSentTo(t, bob, nil)
				fake.AssertNotSentTo(t, alice, func(n contract.Notification) bool {
					return n.(testNotification).tag == "missing"
				})
			},
		},
		{
			name: "AssertNothingSent passes when no Via channels route to fake",
			check: func(t *testing.T, mgr *notification.Manager, fake *FakeChannel) {
				// Notification routes to a different channel name, so the fake
				// records nothing.
				_ = mgr.Send(context.Background(), alice, testNotification{channels: []string{"other"}, tag: "x"})
				fake.AssertNothingSent(t)
			},
		},
		{
			name: "AssertNothingSent passes for empty Via",
			check: func(t *testing.T, mgr *notification.Manager, fake *FakeChannel) {
				_ = mgr.Send(context.Background(), alice, testNotification{channels: nil})
				fake.AssertNothingSent(t)
			},
		},
		{
			name: "GetSent returns recorded notifications in order",
			check: func(t *testing.T, mgr *notification.Manager, fake *FakeChannel) {
				_ = mgr.Send(context.Background(), alice, testNotification{channels: []string{"fake"}, tag: "first"})
				_ = mgr.Send(context.Background(), bob, testNotification{channels: []string{"fake"}, tag: "second"})

				sent := fake.GetSent()
				if len(sent) != 2 {
					t.Fatalf("expected 2 sent, got %d", len(sent))
				}
				if sent[0].(testNotification).tag != "first" || sent[1].(testNotification).tag != "second" {
					t.Errorf("unexpected order: %q, %q", sent[0].(testNotification).tag, sent[1].(testNotification).tag)
				}
			},
		},
		{
			name: "GetSent copy does not alias internal slice",
			check: func(t *testing.T, mgr *notification.Manager, fake *FakeChannel) {
				_ = mgr.Send(context.Background(), alice, testNotification{channels: []string{"fake"}, tag: "first"})

				sent := fake.GetSent()
				sent[0] = nil // mutate the returned copy

				again := fake.GetSent()
				if again[0] == nil {
					t.Errorf("GetSent leaked a reference to internal state")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := NewFakeChannel()
			mgr := notification.NewManager()
			// Route the "fake" channel through the test double via the manager's
			// channel registration; production registration is untouched.
			mgr.SetChannel("fake", fake)

			tt.check(t, mgr, fake)
		})
	}
}
