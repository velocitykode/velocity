package mail

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/trace"
)

// The log driver registers from the mail root so MAIL_DRIVER=log works with
// only package mail imported.
func init() {
	Drivers().Register("log", func(_ context.Context, cfg MailConfig) (Mailer, error) {
		return NewLogDriver(), nil
	})
}

// logDriverMaxEntries bounds the dev/test inspection buffer. It is not an
// audit log.
const logDriverMaxEntries = 100

// LogDriver logs emails instead of sending them (for development). Each
// message is one info line through the driver's logger (the app logger in
// an app built by velocity.New) naming the sender, the recipients, the
// subject, the body sizes and the attachment names, never the body. The
// recipient addresses are key-value values, so the logger's redactors
// (LOG_REDACT, LOG_REDACT_EMAILS) apply to them. Without a logger the line
// goes to the framework's standalone fallback logger, which drops info
// lines. The retained log is a bounded ring of the last logDriverMaxEntries
// entries for dev/test inspection.
type LogDriver struct {
	mu     sync.Mutex
	log    []string
	logger contract.Logger
}

// SetLogger installs the logger each message's summary is written to. Nil
// restores the framework's standalone fallback logger. Safe to call while
// messages are sent.
func (d *LogDriver) SetLogger(l contract.Logger) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.logger = l
}

var _ contract.LoggerAware = (*LogDriver)(nil)

// NewLogDriver creates a new log driver.
func NewLogDriver() *LogDriver {
	return &LogDriver{log: make([]string, 0)}
}

// Send logs the email instead of sending it.
//
// The summary is built and the line written with no lock held: only the
// append to the retained log takes the driver's lock, so a logger that
// reads the log, replaces the logger or sends another message never waits
// on it. A panicking logger falls back to the standalone logger.
func (d *LogDriver) Send(ctx context.Context, msg *Message) error {
	// parts is the retained entry; kvs is the same summary as log
	// key-value pairs.
	var parts []string
	var kvs []any

	from := msg.GetFrom()
	if from.Email != "" {
		parts = append(parts, fmt.Sprintf("From: %s", from.String()))
		kvs = append(kvs, "from", from.String())
	}

	to := msg.GetTo()
	if len(to) > 0 {
		toAddrs := strings.Join(addressEmails(to), ", ")
		parts = append(parts, fmt.Sprintf("To: %s", toAddrs))
		kvs = append(kvs, "to", toAddrs)
	}

	cc := msg.GetCC()
	if len(cc) > 0 {
		ccAddrs := strings.Join(addressEmails(cc), ", ")
		parts = append(parts, fmt.Sprintf("CC: %s", ccAddrs))
		kvs = append(kvs, "cc", ccAddrs)
	}

	bcc := msg.GetBCC()
	if len(bcc) > 0 {
		bccAddrs := strings.Join(addressEmails(bcc), ", ")
		parts = append(parts, fmt.Sprintf("BCC: %s", bccAddrs))
		kvs = append(kvs, "bcc", bccAddrs)
	}

	replyTo := msg.GetReplyTo()
	if len(replyTo) > 0 {
		replyToAddrs := strings.Join(addressEmails(replyTo), ", ")
		parts = append(parts, fmt.Sprintf("Reply-To: %s", replyToAddrs))
		kvs = append(kvs, "reply_to", replyToAddrs)
	}

	subject := msg.GetSubject()
	if subject != "" {
		parts = append(parts, fmt.Sprintf("Subject: %s", subject))
		kvs = append(kvs, "subject", subject)
	}

	textBody := msg.GetTextBody()
	if textBody != "" {
		parts = append(parts, fmt.Sprintf("Text Body: %d bytes", len(textBody)))
		kvs = append(kvs, "text_bytes", len(textBody))
	}

	htmlBody := msg.GetHTMLBody()
	if htmlBody != "" {
		parts = append(parts, fmt.Sprintf("HTML Body: %d bytes", len(htmlBody)))
		kvs = append(kvs, "html_bytes", len(htmlBody))
	}

	attachments := msg.GetAttachments()
	if len(attachments) > 0 {
		attNames := make([]string, len(attachments))
		for i, att := range attachments {
			attNames[i] = att.Name
		}
		parts = append(parts, fmt.Sprintf("Attachments: %s", strings.Join(attNames, ", ")))
		kvs = append(kvs, "attachments", strings.Join(attNames, ", "))
	}

	logEntry := strings.Join(parts, " | ")
	d.mu.Lock()
	d.log = append(d.log, logEntry)
	if len(d.log) > logDriverMaxEntries {
		retained := make([]string, logDriverMaxEntries)
		copy(retained, d.log[len(d.log)-logDriverMaxEntries:])
		d.log = retained
	}
	logger := d.logger
	d.mu.Unlock()

	fallbacklog.Write(logger, func(l contract.Logger) {
		l.With(trace.LogFields(ctx)...).Info("velocity/mail: message logged, not sent (log driver)", kvs...)
	})
	return nil
}

// addressEmails returns the email of each address.
func addressEmails(addrs []Address) []string {
	emails := make([]string, len(addrs))
	for i, addr := range addrs {
		emails[i] = addr.Email
	}
	return emails
}

// GetLog returns all logged emails (for testing).
func (d *LogDriver) GetLog() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	logCopy := make([]string, len(d.log))
	copy(logCopy, d.log)
	return logCopy
}

// ClearLog clears the logged emails (for testing).
func (d *LogDriver) ClearLog() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.log = make([]string, 0)
}
