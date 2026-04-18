package mail

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/velocitykode/velocity/contract"
)

// allowedPostmarkStreams enumerates the Message Streams recognised by default.
// Callers that use custom broadcast streams can configure AllowedPostmarkStreams
// via ConfigureAllowedPostmarkStreams.
var (
	allowedPostmarkStreams   = map[string]struct{}{"outbound": {}, "broadcast": {}, "transactional": {}, "inbound": {}}
	allowedPostmarkStreamsMu sync.RWMutex
)

// ConfigureAllowedPostmarkStreams replaces the set of allowed Postmark message
// streams. Names are lower-cased. Passing an empty slice restores the defaults.
func ConfigureAllowedPostmarkStreams(streams []string) {
	allowedPostmarkStreamsMu.Lock()
	defer allowedPostmarkStreamsMu.Unlock()
	if len(streams) == 0 {
		allowedPostmarkStreams = map[string]struct{}{"outbound": {}, "broadcast": {}, "transactional": {}, "inbound": {}}
		return
	}
	next := make(map[string]struct{}, len(streams))
	for _, s := range streams {
		next[strings.ToLower(strings.TrimSpace(s))] = struct{}{}
	}
	allowedPostmarkStreams = next
}

// IsAllowedPostmarkStream reports whether a stream name passes the allowlist.
func IsAllowedPostmarkStream(name string) bool {
	allowedPostmarkStreamsMu.RLock()
	defer allowedPostmarkStreamsMu.RUnlock()
	_, ok := allowedPostmarkStreams[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

func validateLocalPort(port string) error {
	if port == "" {
		// NewLocalDriver defaults to 587 when Host is set.
		return nil
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("velocity/mail: invalid MAIL_PORT %q: %w", port, err)
	}
	if n < 1 || n > 65535 {
		return fmt.Errorf("velocity/mail: MAIL_PORT out of range: %d", n)
	}
	return nil
}

func validateLocalEncryption(enc string) error {
	if _, ok := allowedLocalEncryptions[strings.ToLower(strings.TrimSpace(enc))]; !ok {
		return fmt.Errorf("velocity/mail: unsupported MAIL_ENCRYPTION %q", enc)
	}
	return nil
}

func validateMailgunEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("velocity/mail: invalid MAIL_MAILGUN_ENDPOINT: %w", err)
	}
	if _, ok := allowedMailgunSchemes[strings.ToLower(u.Scheme)]; !ok {
		return fmt.Errorf("velocity/mail: MAIL_MAILGUN_ENDPOINT must use https, got %q", u.Scheme)
	}
	return nil
}

func validatePostmarkStream(stream string) error {
	if stream == "" {
		// NewPostmarkDriver defaults to "outbound".
		return nil
	}
	if !IsAllowedPostmarkStream(stream) {
		return fmt.Errorf("velocity/mail: postmark message stream %q is not allowed", stream)
	}
	return nil
}

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

// allowedLocalEncryptions enumerates the encryption modes the local SMTP driver
// will accept. Anything outside this set is rejected at config time.
var allowedLocalEncryptions = map[string]struct{}{
	"":         {}, // equivalent to starttls
	"none":     {}, // explicit opt-out — still refuses plain-auth over cleartext
	"starttls": {},
	"tls":      {}, // implicit TLS (SMTPS)
	"ssl":      {}, // legacy alias for implicit TLS
}

// allowedMailgunSchemes are the URL schemes accepted for Mailgun endpoints.
var allowedMailgunSchemes = map[string]struct{}{
	"https": {},
}

// Validate checks that required fields are set for the configured driver.
func (c MailConfig) Validate() error {
	switch c.Driver {
	case "log", "":
		return nil
	case "mailgun":
		if c.Mailgun.Domain == "" {
			return fmt.Errorf("velocity/mail: MAIL_MAILGUN_DOMAIN is required for mailgun driver")
		}
		if c.Mailgun.Secret == "" {
			return fmt.Errorf("velocity/mail: MAIL_MAILGUN_SECRET is required for mailgun driver")
		}
		if c.Mailgun.Endpoint != "" {
			if err := validateMailgunEndpoint(c.Mailgun.Endpoint); err != nil {
				return err
			}
		}
	case "postmark":
		if c.Postmark.Token == "" {
			return fmt.Errorf("velocity/mail: MAIL_POSTMARK_TOKEN is required for postmark driver")
		}
		if err := validatePostmarkStream(c.Postmark.MessageStream); err != nil {
			return err
		}
	case "local":
		if c.Local.SendmailPath == "" && c.Local.Host == "" {
			return fmt.Errorf("velocity/mail: MAIL_HOST or MAIL_SENDMAIL_PATH must be set for local driver")
		}
		if c.Local.Host != "" {
			if err := validateLocalPort(c.Local.Port); err != nil {
				return err
			}
			if err := validateLocalEncryption(c.Local.Encryption); err != nil {
				return err
			}
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
// Panics with *contract.RegistrationError if factory is nil or the driver name
// is already registered.
func RegisterDriver(name string, factory func(MailConfig) (Mailer, error)) {
	if factory == nil {
		panic(contract.NewRegistrationError("mail", fmt.Sprintf("nil factory for %q", name)))
	}
	driverMu.Lock()
	defer driverMu.Unlock()
	if _, exists := driverFactories[name]; exists {
		panic(contract.NewRegistrationError("mail", fmt.Sprintf("driver %q already registered", name)))
	}
	driverFactories[name] = factory
}
