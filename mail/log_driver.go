package mail

import (
	"context"
	"fmt"
	stdlog "log"
	"strings"
	"sync"
)

// The log driver lives inside the mail package (rather than under
// mail/drivers) so it's registered as soon as anything imports the
// mail package. That matches the MAIL_DRIVER=log default — users who
// never opt into a real provider still get a working, zero-dependency
// mailer without needing blank imports in their main.go.
func init() {
	RegisterDriver("log", func() (Mailer, error) {
		return NewLogDriver(), nil
	})
}

// LogDriver logs emails instead of sending them (for development).
type LogDriver struct {
	mu  sync.Mutex
	log []string
}

// NewLogDriver creates a new log driver.
func NewLogDriver() *LogDriver {
	return &LogDriver{log: make([]string, 0)}
}

// Send logs the email instead of sending it.
func (d *LogDriver) Send(ctx context.Context, msg *Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	var parts []string

	from := msg.GetFrom()
	if from.Email != "" {
		parts = append(parts, fmt.Sprintf("From: %s", from.String()))
	}

	to := msg.GetTo()
	if len(to) > 0 {
		toAddrs := make([]string, len(to))
		for i, addr := range to {
			toAddrs[i] = addr.Email
		}
		parts = append(parts, fmt.Sprintf("To: %s", strings.Join(toAddrs, ", ")))
	}

	cc := msg.GetCC()
	if len(cc) > 0 {
		ccAddrs := make([]string, len(cc))
		for i, addr := range cc {
			ccAddrs[i] = addr.Email
		}
		parts = append(parts, fmt.Sprintf("CC: %s", strings.Join(ccAddrs, ", ")))
	}

	bcc := msg.GetBCC()
	if len(bcc) > 0 {
		bccAddrs := make([]string, len(bcc))
		for i, addr := range bcc {
			bccAddrs[i] = addr.Email
		}
		parts = append(parts, fmt.Sprintf("BCC: %s", strings.Join(bccAddrs, ", ")))
	}

	replyTo := msg.GetReplyTo()
	if len(replyTo) > 0 {
		replyToAddrs := make([]string, len(replyTo))
		for i, addr := range replyTo {
			replyToAddrs[i] = addr.Email
		}
		parts = append(parts, fmt.Sprintf("Reply-To: %s", strings.Join(replyToAddrs, ", ")))
	}

	subject := msg.GetSubject()
	if subject != "" {
		parts = append(parts, fmt.Sprintf("Subject: %s", subject))
	}

	textBody := msg.GetTextBody()
	if textBody != "" {
		parts = append(parts, fmt.Sprintf("Text Body: %d bytes", len(textBody)))
	}

	htmlBody := msg.GetHTMLBody()
	if htmlBody != "" {
		parts = append(parts, fmt.Sprintf("HTML Body: %d bytes", len(htmlBody)))
	}

	attachments := msg.GetAttachments()
	if len(attachments) > 0 {
		attNames := make([]string, len(attachments))
		for i, att := range attachments {
			attNames[i] = att.Name
		}
		parts = append(parts, fmt.Sprintf("Attachments: %s", strings.Join(attNames, ", ")))
	}

	logEntry := strings.Join(parts, " | ")
	d.log = append(d.log, logEntry)
	stdlog.Printf("[MAIL] %s", logEntry)
	return nil
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
