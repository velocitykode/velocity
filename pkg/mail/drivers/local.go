package drivers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/smtp"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/velocitykode/velocity/pkg/mail"
)

func init() {
	mail.RegisterDriver("local", func() (mail.Mailer, error) {
		return NewLocalDriver()
	})
}

// LocalDriver sends emails via SMTP or sendmail.
// The username and password fields contain sensitive credentials and must not be logged.
type LocalDriver struct {
	host       string
	port       string
	username   string // SENSITIVE: do not log
	password   string // SENSITIVE: do not log
	encryption string
	sendmail   string
	mu         sync.Mutex
}

// String returns a safe representation with credentials redacted.
func (d *LocalDriver) String() string {
	return fmt.Sprintf("LocalDriver{Host:%s, Port:%s, Username:[REDACTED], Password:[REDACTED]}", d.host, d.port)
}

// NewLocalDriver creates a new local SMTP/sendmail driver
func NewLocalDriver() (*LocalDriver, error) {
	driver := &LocalDriver{
		host:       os.Getenv("MAIL_HOST"),
		port:       os.Getenv("MAIL_PORT"),
		username:   os.Getenv("MAIL_USERNAME"),
		password:   os.Getenv("MAIL_PASSWORD"),
		encryption: os.Getenv("MAIL_ENCRYPTION"),
		sendmail:   os.Getenv("MAIL_SENDMAIL_PATH"),
	}

	// Validate configuration
	if driver.sendmail == "" && driver.host == "" {
		return nil, fmt.Errorf("mail: MAIL_HOST or MAIL_SENDMAIL_PATH must be set for local driver")
	}

	if driver.port == "" && driver.host != "" {
		driver.port = "587" // Default SMTP port
	}

	return driver, nil
}

// Send sends an email via SMTP or sendmail
func (d *LocalDriver) Send(ctx context.Context, msg *mail.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.sendmail != "" {
		return d.sendViaSendmail(ctx, msg)
	}
	return d.sendViaSMTP(ctx, msg)
}

// sendViaSMTP sends email via SMTP
func (d *LocalDriver) sendViaSMTP(ctx context.Context, msg *mail.Message) error {
	// Build message
	body := d.buildMessage(msg)

	// Collect all recipients
	recipients := make([]string, 0)
	for _, addr := range msg.GetTo() {
		recipients = append(recipients, addr.Email)
	}
	for _, addr := range msg.GetCC() {
		recipients = append(recipients, addr.Email)
	}
	for _, addr := range msg.GetBCC() {
		recipients = append(recipients, addr.Email)
	}

	if len(recipients) == 0 {
		return fmt.Errorf("mail: no recipients specified")
	}

	// Setup authentication
	var auth smtp.Auth
	if d.username != "" {
		auth = smtp.PlainAuth("", d.username, d.password, d.host)
	}

	// Send email
	addr := fmt.Sprintf("%s:%s", d.host, d.port)
	from := msg.GetFrom().Email
	if from == "" {
		from = os.Getenv("MAIL_FROM_ADDRESS")
	}

	return smtp.SendMail(addr, auth, from, recipients, body)
}

// sendViaSendmail sends email via sendmail command
func (d *LocalDriver) sendViaSendmail(ctx context.Context, msg *mail.Message) error {
	// Build message
	body := d.buildMessage(msg)

	// Collect recipients for sendmail
	recipients := make([]string, 0)
	for _, addr := range msg.GetTo() {
		recipients = append(recipients, addr.Email)
	}
	for _, addr := range msg.GetCC() {
		recipients = append(recipients, addr.Email)
	}
	for _, addr := range msg.GetBCC() {
		recipients = append(recipients, addr.Email)
	}

	if len(recipients) == 0 {
		return fmt.Errorf("mail: no recipients specified")
	}

	// Execute sendmail
	cmd := exec.CommandContext(ctx, d.sendmail, "-t", "-i")
	cmd.Stdin = bytes.NewReader(body)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mail: sendmail failed: %w, output: %s", err, string(output))
	}

	return nil
}

// sanitizeHeader strips CRLF characters to prevent header injection
func sanitizeHeader(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	return value
}

// sanitizeFilename removes characters that could cause injection in MIME headers
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, "\"", "")
	name = strings.ReplaceAll(name, "\\", "")
	return name
}

// generateBoundary generates a random MIME boundary
func generateBoundary() string {
	b := make([]byte, 24)
	_, err := rand.Read(b)
	if err != nil {
		// Extremely unlikely; fall back to a unique-enough value
		return fmt.Sprintf("----VelocityMail%d", b[0])
	}
	return "----VelocityMail" + base64.RawURLEncoding.EncodeToString(b)
}

// buildMessage builds the RFC 822 email message
func (d *LocalDriver) buildMessage(msg *mail.Message) []byte {
	var buf bytes.Buffer

	// From header
	from := msg.GetFrom()
	if from.Email == "" {
		from.Email = os.Getenv("MAIL_FROM_ADDRESS")
		from.Name = os.Getenv("MAIL_FROM_NAME")
	}
	buf.WriteString(fmt.Sprintf("From: %s\r\n", sanitizeHeader(from.String())))

	// To header
	to := msg.GetTo()
	if len(to) > 0 {
		toAddrs := make([]string, len(to))
		for i, addr := range to {
			toAddrs[i] = sanitizeHeader(addr.String())
		}
		buf.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(toAddrs, ", ")))
	}

	// CC header
	cc := msg.GetCC()
	if len(cc) > 0 {
		ccAddrs := make([]string, len(cc))
		for i, addr := range cc {
			ccAddrs[i] = sanitizeHeader(addr.String())
		}
		buf.WriteString(fmt.Sprintf("Cc: %s\r\n", strings.Join(ccAddrs, ", ")))
	}

	// Reply-To header
	replyTo := msg.GetReplyTo()
	if len(replyTo) > 0 {
		replyToAddrs := make([]string, len(replyTo))
		for i, addr := range replyTo {
			replyToAddrs[i] = sanitizeHeader(addr.String())
		}
		buf.WriteString(fmt.Sprintf("Reply-To: %s\r\n", strings.Join(replyToAddrs, ", ")))
	}

	// Subject header
	buf.WriteString(fmt.Sprintf("Subject: %s\r\n", sanitizeHeader(msg.GetSubject())))

	// Priority header
	switch msg.GetPriority() {
	case mail.HighPriority:
		buf.WriteString("X-Priority: 1\r\n")
	case mail.LowPriority:
		buf.WriteString("X-Priority: 5\r\n")
	}

	// Custom headers
	for key, value := range msg.GetHeaders() {
		buf.WriteString(fmt.Sprintf("%s: %s\r\n", sanitizeHeader(key), sanitizeHeader(value)))
	}

	// MIME headers
	buf.WriteString("MIME-Version: 1.0\r\n")

	attachments := msg.GetAttachments()
	if len(attachments) > 0 {
		boundary := generateBoundary()
		buf.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=%s\r\n\r\n", boundary))

		// Text/HTML part
		buf.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		d.writeBody(&buf, msg)

		// Attachments
		for _, att := range attachments {
			safeName := sanitizeFilename(att.Name)
			buf.WriteString(fmt.Sprintf("\r\n--%s\r\n", boundary))
			buf.WriteString(fmt.Sprintf("Content-Type: %s; name=\"%s\"\r\n", sanitizeHeader(att.ContentType), safeName))
			buf.WriteString("Content-Transfer-Encoding: base64\r\n")
			buf.WriteString(fmt.Sprintf("Content-Disposition: attachment; filename=\"%s\"\r\n\r\n", safeName))

			encoded := base64.StdEncoding.EncodeToString(att.Data)
			// Split into 76 character lines
			for i := 0; i < len(encoded); i += 76 {
				end := i + 76
				if end > len(encoded) {
					end = len(encoded)
				}
				buf.WriteString(encoded[i:end] + "\r\n")
			}
		}

		buf.WriteString(fmt.Sprintf("\r\n--%s--\r\n", boundary))
	} else {
		d.writeBody(&buf, msg)
	}

	return buf.Bytes()
}

// writeBody writes the text/HTML body
func (d *LocalDriver) writeBody(buf *bytes.Buffer, msg *mail.Message) {
	textBody := msg.GetTextBody()
	htmlBody := msg.GetHTMLBody()

	if htmlBody != "" && textBody != "" {
		// Both text and HTML
		boundary := generateBoundary()
		buf.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=%s\r\n\r\n", boundary))

		// Text part
		buf.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		buf.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
		buf.WriteString(textBody + "\r\n")

		// HTML part
		buf.WriteString(fmt.Sprintf("\r\n--%s\r\n", boundary))
		buf.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
		buf.WriteString(htmlBody + "\r\n")

		buf.WriteString(fmt.Sprintf("\r\n--%s--\r\n", boundary))
	} else if htmlBody != "" {
		// HTML only
		buf.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
		buf.WriteString(htmlBody + "\r\n")
	} else {
		// Text only
		buf.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
		buf.WriteString(textBody + "\r\n")
	}
}
