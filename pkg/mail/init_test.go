package mail_test

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/pkg/mail"

	// Import drivers to register them
	_ "github.com/velocitykode/velocity/pkg/mail/drivers"
)

func TestNewMailer(t *testing.T) {
	t.Run("with log driver", func(t *testing.T) {
		mailer, err := mail.NewMailer(mail.MailConfig{Driver: "log"})
		if err != nil {
			t.Errorf("Expected no error creating log mailer, got %v", err)
		}

		if mailer == nil {
			t.Error("Expected mailer to be created")
		}
	})

	t.Run("with empty driver falls back to log", func(t *testing.T) {
		mailer, err := mail.NewMailer(mail.MailConfig{Driver: ""})
		if err != nil {
			t.Errorf("Expected no error with empty driver, got %v", err)
		}

		if mailer == nil {
			t.Error("Expected mailer to be created")
		}
	})
}

func TestNewMailerWithInvalidDriver(t *testing.T) {
	_, err := mail.NewMailer(mail.MailConfig{Driver: "invalid"})
	if err == nil {
		t.Error("Expected error creating mailer with invalid driver")
	}
}

type mockMailer struct{}

func (m *mockMailer) Send(ctx context.Context, msg *mail.Message) error {
	return nil
}

func TestRegisterDriver(t *testing.T) {
	mail.RegisterDriver("custom", func() (mail.Mailer, error) {
		return &mockMailer{}, nil
	})

	mailer, err := mail.NewMailer(mail.MailConfig{Driver: "custom"})
	if err != nil {
		t.Errorf("Expected no error with custom driver, got %v", err)
	}

	if mailer == nil {
		t.Error("Expected mailer to be created from custom driver")
	}
}
