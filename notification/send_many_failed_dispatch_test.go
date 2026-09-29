package notification

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/hostile"
)

// A channel that panics in SendMany is reported to the caller even when
// the NotificationFailed dispatch for it runs hostile code: the error is
// in SendMany's result whether the dispatcher or the failure hook panics.
func TestSendMany_PanicErrorSurvivesAHostileFailedDispatch(t *testing.T) {
	for _, where := range []string{"dispatcher", "hook"} {
		t.Run(where, func(t *testing.T) {
			code := hostile.New(t, hostile.Panic, nil)
			m := NewManager()
			m.SetChannel("panic", panicChannel{})
			m.SetEventDispatcher(func(_ context.Context, event interface{}) error {
				if _, ok := event.(*NotificationFailed); ok {
					if where == "dispatcher" {
						code.Run()
					}
					return errors.New("listener failed")
				}
				return nil
			})
			if where == "hook" {
				failures := &eventemit.Failures{}
				failures.SetHook(func(error, any) { code.Run() })
				m.events.Share(failures)
			}
			n := &testNotification{subject: "boom", channels: []string{"panic"}}
			var err error
			if p := hostile.Within(t, hostile.Deadline, func() {
				err = m.SendMany(context.Background(), []interface{}{&testNotifiable{email: "a@example.com"}}, n)
			}); p != nil {
				t.Fatalf("SendMany panicked: %v", p)
			}
			var rp contract.RecoveredPanic
			if !errors.As(err, &rp) {
				t.Errorf("SendMany = %v, want the channel's panic as a contract.RecoveredPanic", err)
			}
		})
	}
}
