package events

import (
	"testing"

	"github.com/velocitykode/velocity/contract"
)

type nilListener struct{ Listener }

// Listen refuses a typed-nil listener at registration, as it refuses nil,
// not at the first dispatch.
func TestListen_RefusesATypedNilListener(t *testing.T) {
	var typed *nilListener
	defer func() {
		p := recover()
		if _, ok := p.(*contract.RegistrationError); !ok {
			t.Fatalf("Listen(typed nil) panicked with %v (%T), want *contract.RegistrationError", p, p)
		}
	}()
	NewDispatcher().Listen("user.created", typed)
}
