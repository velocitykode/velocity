package mail

import (
	"context"
	"fmt"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/driverregistry"
	"github.com/velocitykode/velocity/internal/errchain"
)

// drivers is the canonical Velocity driver registry for mail. Driver
// authors call Drivers().Register("name", factory) from an init(). The
// resolver in NewMailer reads through this registry.
var drivers = driverregistry.New[Mailer, Config]("mail")

// Drivers returns the registry that mail drivers register themselves into.
// Use this from a driver package's init() to install a factory:
//
//	func init() {
//	    mail.Drivers().Register("postmark", func(ctx context.Context, cfg mail.Config) (mail.Mailer, error) {
//	        return NewPostmarkDriver(cfg.Postmark, cfg.FromAddress, cfg.FromName)
//	    })
//	}
func Drivers() *driverregistry.Registry[Mailer, Config] { return drivers }

// Config holds configuration for creating a mailer.
type Config struct {
	// Driver is the mail driver to use (e.g. "log", "postmark", "mailgun",
	// "local") (MAIL_DRIVER, default "log").
	Driver string

	// FromAddress is the default sender email address (MAIL_FROM_ADDRESS).
	FromAddress string

	// FromName is the default sender display name (MAIL_FROM_NAME, default
	// "").
	FromName string

	// Local holds the local SMTP/sendmail driver settings.
	Local LocalConfig

	// Mailgun holds the Mailgun driver settings.
	Mailgun MailgunConfig

	// MaxAttachmentSize is the maximum per-attachment size in bytes accepted
	// by Message.AttachFile / Message.AttachData. A zero (or negative) value
	// means "use DefaultMaxAttachmentSize"; it does NOT mean "unlimited".
	// The default (25 MiB) matches common SMTP provider limits: SES 40 MB,
	// SendGrid 30 MB, Postmark 10 MB, Mailgun 25 MB
	// (MAIL_MAX_ATTACHMENT_SIZE, default DefaultMaxAttachmentSize).
	MaxAttachmentSize int64

	// Postmark holds the Postmark driver settings.
	Postmark PostmarkConfig
}

// Validate checks the Config for structural problems. An empty Driver
// is accepted; NewMailer defaults to "log". Per-driver credential checks
// (Mailgun.Secret, Postmark.Token, Local.Host) are not enforced here so
// test fixtures can construct partial configs; missing credentials will
// surface at Send time via the driver's own error path.
func (c Config) Validate() error {
	if c.Driver != "" {
		if err := drivers.Check(c.Driver); err != nil {
			return err
		}
	}
	if c.MaxAttachmentSize < 0 {
		return fmt.Errorf("velocity/mail: MAIL_MAX_ATTACHMENT_SIZE must be non-negative, got %d", c.MaxAttachmentSize)
	}
	return nil
}

// MailgunConfig holds Mailgun-specific configuration.
type MailgunConfig struct {
	// Domain is the Mailgun sending domain (MAIL_MAILGUN_DOMAIN, default
	// "").
	Domain string

	// Endpoint is the Mailgun API base URL. The mailgun driver uses
	// "https://api.mailgun.net/v3" when it is empty and rejects any scheme
	// other than https (MAIL_MAILGUN_ENDPOINT).
	Endpoint string

	// Secret is the Mailgun API key. SENSITIVE: do not log
	// (MAIL_MAILGUN_SECRET).
	Secret string

	// WebhookSigningKey is the Mailgun webhook signing key. SENSITIVE: do
	// not log (MAIL_MAILGUN_WEBHOOK_SIGNING_KEY).
	WebhookSigningKey string
}

// PostmarkConfig holds Postmark-specific configuration.
type PostmarkConfig struct {
	// MessageStream is the Postmark message stream to send through. The
	// postmark driver uses "outbound" when it is empty and rejects a stream
	// not in its allowlist (MAIL_POSTMARK_MESSAGE_STREAM).
	MessageStream string

	// Token is the Postmark server API token. SENSITIVE: do not log
	// (MAIL_POSTMARK_TOKEN).
	Token string
}

// LocalConfig holds local SMTP/sendmail configuration.
type LocalConfig struct {
	// Encryption is the SMTP encryption mode. "tls" or "ssl" (case
	// insensitive) means implicit TLS; any other value means the server
	// must offer STARTTLS before credentials are sent (MAIL_ENCRYPTION).
	Encryption string

	// Host is the SMTP server host (MAIL_HOST).
	Host string

	// Password is the SMTP password. SENSITIVE: do not log (MAIL_PASSWORD).
	Password string

	// Port is the SMTP server port. The local driver uses "587" when it is
	// empty and Host is set (MAIL_PORT).
	Port string

	// SendmailPath is the path of the sendmail binary. When set, the local
	// driver sends through sendmail instead of SMTP (MAIL_SENDMAIL_PATH).
	SendmailPath string

	// Username is the SMTP username. SENSITIVE: do not log (MAIL_USERNAME).
	Username string
}

// NewMailer creates a new Mailer from the given configuration.
// Drivers must be registered via Drivers().Register before calling this
// function (typically through a blank import of mail/standard).
//
// NewMailer does NOT mutate any process-wide state. To promote a config's
// MaxAttachmentSize to the package-level default used by NewMessage, call
// SetDefaultMaxAttachmentSize once at the app's single construction site.
func NewMailer(config Config) (Mailer, error) {
	return NewMailerWithContext(context.Background(), config)
}

// NewMailerWithContext is the context-aware variant of NewMailer. The ctx
// is forwarded to the driver factory so drivers that perform network I/O
// during construction can honour deadlines.
//
// Like NewMailer, it does NOT mutate any process-wide state and never calls
// SetDefaultMaxAttachmentSize. To promote a config's MaxAttachmentSize to the
// package-level default used by NewMessage, call SetDefaultMaxAttachmentSize
// once at the app's single construction site.
func NewMailerWithContext(ctx context.Context, config Config) (Mailer, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	driver := config.Driver
	if driver == "" {
		driver = "log"
	}

	m, err := drivers.Resolve(ctx, driver, config)
	if err != nil {
		return nil, errchain.Errorf("velocity/mail: %w", err)
	}
	return &checkedMailer{inner: m}, nil
}

// checkedMailer wraps a Mailer so that any deferred error accumulated on a
// *Message (from AttachFile, AttachData, Header, Subject, To, etc.) is
// surfaced from Send before the driver ever sees the message. This is the
// backstop for callers that ignore the fluent setters' errors.
type checkedMailer struct {
	inner Mailer
}

func (cm *checkedMailer) Send(ctx context.Context, msg *Message) error {
	// Guard nil before forwarding to the driver, which would dereference it.
	if msg == nil {
		return ErrNilMessage
	}
	if err := msg.Err(); err != nil {
		return err
	}
	return cm.inner.Send(ctx, msg)
}

// Shutdown forwards to the inner mailer when it implements ShutdownableMailer.
func (cm *checkedMailer) Shutdown(ctx context.Context) error {
	if sd, ok := cm.inner.(ShutdownableMailer); ok {
		return sd.Shutdown(ctx)
	}
	return nil
}

// SetLogger forwards to the inner mailer when it takes a logger
// (contract.LoggerAware), such as the log driver; otherwise it does
// nothing.
func (cm *checkedMailer) SetLogger(l contract.Logger) {
	if la, ok := cm.inner.(contract.LoggerAware); ok {
		la.SetLogger(l)
	}
}
