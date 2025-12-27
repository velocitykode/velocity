package mail_test

import (
	"context"
	"os"
	"testing"

	"github.com/velocitykode/velocity/pkg/mail"

	// Import drivers to register them
	_ "github.com/velocitykode/velocity/pkg/mail/drivers"
)

func TestReinitialize(t *testing.T) {
	t.Run("with log driver", func(t *testing.T) {
		// Set to log driver
		os.Setenv("MAIL_DRIVER", "log")

		err := mail.Reinitialize()
		if err != nil {
			t.Errorf("Expected no error reinitializing with log driver, got %v", err)
		}

		if mail.GetDefaultMailer() == nil {
			t.Error("Expected default mailer to be set after reinitialize")
		}
	})

	t.Run("with empty driver falls back to log", func(t *testing.T) {
		os.Unsetenv("MAIL_DRIVER")

		err := mail.Reinitialize()
		if err != nil {
			t.Errorf("Expected no error with empty driver, got %v", err)
		}

		if mail.GetDefaultMailer() == nil {
			t.Error("Expected default mailer to be set")
		}
	})
}

func TestReinitializeWithDriver(t *testing.T) {
	err := mail.ReinitializeWithDriver("log")
	if err != nil {
		t.Errorf("Expected no error reinitializing with log driver, got %v", err)
	}

	if mail.GetDefaultMailer() == nil {
		t.Error("Expected default mailer to be set after reinitialize")
	}
}

func TestReinitializeWithInvalidDriver(t *testing.T) {
	err := mail.ReinitializeWithDriver("invalid")
	if err == nil {
		t.Error("Expected error reinitializing with invalid driver")
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

	err := mail.ReinitializeWithDriver("custom")
	if err != nil {
		t.Errorf("Expected no error with custom driver, got %v", err)
	}
}
