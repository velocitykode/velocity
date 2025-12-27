package mail

import (
	"context"
)

// Default mailer instance
var defaultMailer Mailer

// Send sends an email using the default mailer
func Send(ctx context.Context, msg *Message) error {
	ensureInitialized()
	return defaultMailer.Send(ctx, msg)
}

// SetDefaultMailer sets the default mailer
func SetDefaultMailer(mailer Mailer) {
	defaultMailer = mailer
}

// GetDefaultMailer returns the default mailer
func GetDefaultMailer() Mailer {
	return defaultMailer
}
