package mail

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/velocitykode/velocity/driverregistry"
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

// drivers is the canonical Velocity driver registry for mail. Driver
// authors call Drivers().Register("name", factory) from an init(). The
// resolver in NewMailer reads through this registry.
var drivers = driverregistry.New[Mailer, MailConfig]("mail")

// Drivers returns the registry that mail drivers register themselves into.
// Use this from a driver package's init() to install a factory:
//
//	func init() {
//	    mail.Drivers().Register("postmark", func(ctx context.Context, cfg mail.MailConfig) (mail.Mailer, error) {
//	        return NewPostmarkDriver(cfg.Postmark, cfg.FromAddress, cfg.FromName)
//	    })
//	}
func Drivers() *driverregistry.Registry[Mailer, MailConfig] { return drivers }

// MailConfig holds configuration for creating a mailer.
type MailConfig struct {
	// Driver is the mail driver to use (e.g. "log", "postmark", "mailgun", "local").
	Driver      string
	FromAddress string
	FromName    string

	// MaxAttachmentSize is the maximum per-attachment size in bytes accepted
	// by Message.AttachFile / Message.AttachData. A zero (or negative) value
	// means "use DefaultMaxAttachmentSize"; it does NOT mean "unlimited".
	// The default (25 MiB) matches common SMTP provider limits: SES 40 MB,
	// SendGrid 30 MB, Postmark 10 MB, Mailgun 25 MB.
	MaxAttachmentSize int64

	Mailgun  MailgunConfig
	Postmark PostmarkConfig
	Local    LocalConfig
}

// Validate checks the MailConfig for structural problems. An empty Driver
// is accepted; NewMailer defaults to "log". Per-driver credential checks
// (Mailgun.Secret, Postmark.Token, Local.Host) are not enforced here so
// test fixtures can construct partial configs; missing credentials will
// surface at Send time via the driver's own error path.
func (c MailConfig) Validate() error {
	if c.MaxAttachmentSize < 0 {
		return fmt.Errorf("velocity/mail: MAIL_MAX_ATTACHMENT_SIZE must be non-negative, got %d", c.MaxAttachmentSize)
	}
	return nil
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

// NewMailer creates a new Mailer from the given configuration.
// Drivers must be registered via Drivers().Register before calling this
// function (typically through a blank import of mail/alldrivers).
//
// As a side-effect, NewMailer promotes config.MaxAttachmentSize (or the
// DefaultMaxAttachmentSize when zero/negative) to the package-level default
// used by NewMessage. This means freshly-constructed *Message values inherit
// the limit configured for the app without having to thread it explicitly.
func NewMailer(config MailConfig) (Mailer, error) {
	return NewMailerWithContext(context.Background(), config)
}

// NewMailerWithContext is the context-aware variant of NewMailer. The ctx
// is forwarded to the driver factory so drivers that perform network I/O
// during construction can honour deadlines.
func NewMailerWithContext(ctx context.Context, config MailConfig) (Mailer, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	driver := config.Driver
	if driver == "" {
		driver = "log"
	}

	// Promote config limit to the package default so NewMessage() picks it up.
	SetDefaultMaxAttachmentSize(config.MaxAttachmentSize)

	m, err := drivers.Resolve(ctx, driver, config)
	if err != nil {
		return nil, fmt.Errorf("velocity/mail: %w", err)
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
	if msg != nil {
		if err := msg.Err(); err != nil {
			return err
		}
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
