package mail

import (
	"fmt"
	"sync"
)

var (
	driverFactories = make(map[string]func(MailConfig) (Mailer, error))
	driverMu        sync.RWMutex
)

// MailConfig holds configuration for creating a mailer.
type MailConfig struct {
	// Driver is the mail driver to use (e.g. "log", "postmark", "mailgun", "local").
	Driver      string
	FromAddress string
	FromName    string

	Mailgun  MailgunConfig
	Postmark PostmarkConfig
	Local    LocalConfig
}

// MailgunConfig holds Mailgun-specific configuration.
type MailgunConfig struct {
	Domain            string
	Secret            string // SENSITIVE: do not log
	Endpoint          string
	WebhookSigningKey string // SENSITIVE: do not log
}

// PostmarkConfig holds Postmark-specific configuration.
type PostmarkConfig struct {
	Token         string // SENSITIVE: do not log
	MessageStream string
}

// LocalConfig holds local SMTP/sendmail configuration.
type LocalConfig struct {
	Host         string
	Port         string
	Username     string // SENSITIVE: do not log
	Password     string // SENSITIVE: do not log
	Encryption   string
	SendmailPath string
}

// DefaultConfig returns a MailConfig with sensible defaults (log driver).
func DefaultConfig() MailConfig {
	return MailConfig{
		Driver: "log",
	}
}

// Validate checks that required fields are set for the configured driver.
func (c MailConfig) Validate() error {
	switch c.Driver {
	case "log", "":
		return nil
	case "mailgun":
		if c.Mailgun.Domain == "" {
			return fmt.Errorf("mail: MAIL_MAILGUN_DOMAIN is required for mailgun driver")
		}
		if c.Mailgun.Secret == "" {
			return fmt.Errorf("mail: MAIL_MAILGUN_SECRET is required for mailgun driver")
		}
	case "postmark":
		if c.Postmark.Token == "" {
			return fmt.Errorf("mail: MAIL_POSTMARK_TOKEN is required for postmark driver")
		}
	case "local":
		if c.Local.SendmailPath == "" && c.Local.Host == "" {
			return fmt.Errorf("mail: MAIL_HOST or MAIL_SENDMAIL_PATH must be set for local driver")
		}
	}
	return nil
}

// NewMailer creates a new Mailer from the given configuration.
// Drivers must be registered via RegisterDriver before calling this function.
func NewMailer(config MailConfig) (Mailer, error) {
	driver := config.Driver
	if driver == "" {
		driver = "log"
	}

	driverMu.RLock()
	factory, exists := driverFactories[driver]
	driverMu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("unsupported mail driver: %s (driver not registered)", driver)
	}

	return factory(config)
}

// RegisterDriver allows drivers to register themselves.
func RegisterDriver(name string, factory func(MailConfig) (Mailer, error)) {
	driverMu.Lock()
	defer driverMu.Unlock()
	driverFactories[name] = factory
}
