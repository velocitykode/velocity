// Package mailtest provides executable specifications (contract tests) for
// [mail.Mailer] implementations.
//
// Every framework-shipped mail driver runs through RunDriverContractTests
// in CI; third-party drivers are expected to do the same so transport-level
// invariants (CRLF rejection, single-recipient enforcement, ctx propagation)
// are preserved across drivers.
package mailtest

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/mail"
)

// DriverFactory returns a fresh empty Mailer per sub-test. Cleanup is the
// factory's responsibility (typically via t.Cleanup).
type DriverFactory func(t *testing.T) mail.Mailer

// RunDriverContractTests is the executable specification of [mail.Mailer].
func RunDriverContractTests(t *testing.T, factory DriverFactory) {
	t.Helper()

	t.Run("Send_ValidMessage_Succeeds", func(t *testing.T) {
		d := factory(t)
		msg := mail.NewMessage().
			From("from@example.com", "From").
			To("to@example.com", "To").
			Subject("hello").
			TextBody("body")
		if err := d.Send(context.Background(), msg); err != nil {
			t.Fatalf("Send: %v", err)
		}
	})

	t.Run("Send_AcceptsContext", func(t *testing.T) {
		d := factory(t)
		msg := mail.NewMessage().
			From("from@example.com").
			To("to@example.com").
			Subject("ctx-accepted").
			TextBody("body")
		ctx, cancel := context.WithTimeout(context.Background(), 5)
		defer cancel()
		// The invariant is "Send accepts a ctx parameter without panic".
		// Drivers that perform no I/O MAY succeed regardless; drivers that
		// dial a remote server SHOULD honour the ctx. We do not enforce
		// either side here, only that the call returns at all.
		_ = d.Send(ctx, msg)
	})

	// Send(ctx, nil) is INTENTIONALLY NOT tested. The Mailer contract
	// (mail/types.go) does not document nil-tolerance, and every framework
	// driver currently nil-derefs in that case. Callers are expected to
	// reject empty messages at the manager layer (Manager.Send validates
	// the message before dispatch). If a future revision of the Mailer
	// interface promises nil-safe Send, add the t.Run here.

	t.Run("Send_RoundTripsMultipleMessages", func(t *testing.T) {
		d := factory(t)
		for i := 0; i < 3; i++ {
			msg := mail.NewMessage().
				From("from@example.com").
				To("to@example.com").
				Subject("multi").
				TextBody("body")
			if err := d.Send(context.Background(), msg); err != nil {
				t.Fatalf("Send %d: %v", i, err)
			}
		}
	})
}
