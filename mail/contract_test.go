package mail_test

import (
	"testing"

	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/mail/mailtest"
)

// TestLogDriver_Contract runs the mailtest spec against the in-package log
// driver. The log driver writes to the local log buffer and stdlib log, so
// it exercises the same Send contract path as production drivers (without
// touching the network).
func TestLogDriver_Contract(t *testing.T) {
	mailtest.RunDriverContractTests(t, func(t *testing.T) mail.Mailer {
		return mail.NewLogDriver()
	})
}
