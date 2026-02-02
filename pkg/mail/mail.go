package mail

import (
	"context"
	"time"
)

// Default mailer instance
var defaultMailer Mailer

// Send sends an email using the default mailer
func Send(ctx context.Context, msg *Message) error {
	ensureInitialized()

	// Extract recipient emails for event dispatching
	toAddresses := msg.GetTo()
	toEmails := make([]string, len(toAddresses))
	for i, addr := range toAddresses {
		toEmails[i] = addr.Email
	}
	subject := msg.GetSubject()

	start := time.Now()
	err := defaultMailer.Send(ctx, msg)
	duration := time.Since(start)

	if err != nil {
		dispatchMailFailed(ctx, toEmails, subject, "default", err, duration)
		return err
	}

	dispatchMailSent(ctx, toEmails, subject, "default", duration)
	return nil
}

// SetDefaultMailer sets the default mailer
func SetDefaultMailer(mailer Mailer) {
	defaultMailer = mailer
}

// GetDefaultMailer returns the default mailer
func GetDefaultMailer() Mailer {
	return defaultMailer
}
