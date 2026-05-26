package drivers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"os/exec"
	"strings"
	"sync"

	"github.com/velocitykode/velocity/mail"
)

// newContextDialer returns a dial function bound to ctx so TCP dials are
// cancellable.
func newContextDialer(ctx context.Context) func(network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return func(network, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, addr)
	}
}

func init() {
	mail.Drivers().Register("local", func(_ context.Context, cfg mail.MailConfig) (mail.Mailer, error) {
		return NewLocalDriver(cfg.Local, cfg.FromAddress, cfg.FromName)
	})
}

// ErrPlainAuthRefused is returned when the configured SMTP server does not
// advertise STARTTLS and PlainAuth would expose credentials in cleartext.
var ErrPlainAuthRefused = errors.New("velocity/mail: plain-auth refused")

// LocalDriver sends emails via SMTP or sendmail.
// The username and password fields contain sensitive credentials and must not be logged.
type LocalDriver struct {
	host       string
	port       string
	username   string // SENSITIVE: do not log
	password   string // SENSITIVE: do not log
	encryption string
	sendmail   string
	fromAddr   string
	fromName   string
	mu         sync.Mutex
}

// String returns a safe representation with credentials redacted.
func (d *LocalDriver) String() string {
	return fmt.Sprintf("LocalDriver{Host:%s, Port:%s, Username:[REDACTED], Password:[REDACTED]}", d.host, d.port)
}

// NewLocalDriver creates a new local SMTP/sendmail driver from the provided config.
func NewLocalDriver(config mail.LocalConfig, fromAddr, fromName string) (*LocalDriver, error) {
	if config.SendmailPath == "" && config.Host == "" {
		return nil, fmt.Errorf("velocity/mail: MAIL_HOST or MAIL_SENDMAIL_PATH must be set for local driver")
	}

	port := config.Port
	if port == "" && config.Host != "" {
		port = "587"
	}

	return &LocalDriver{
		host:       config.Host,
		port:       port,
		username:   config.Username,
		password:   config.Password,
		encryption: config.Encryption,
		sendmail:   config.SendmailPath,
		fromAddr:   fromAddr,
		fromName:   fromName,
	}, nil
}

// Send sends an email via SMTP or sendmail
func (d *LocalDriver) Send(ctx context.Context, msg *mail.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Defence in depth: reject any address that carries CR/LF before
	// the SMTP envelope is built. Setter-built messages pass this
	// trivially (validateAddressField blocks the same characters);
	// literal-constructed mail.Address values are the failure mode.
	if err := validateMessageAddresses(msg); err != nil {
		return err
	}

	if d.sendmail != "" {
		return d.sendViaSendmail(ctx, msg)
	}
	return d.sendViaSMTP(ctx, msg)
}

// validateMessageAddresses calls mail.Address.Validate on every address
// field of the message so that a literal-constructed Address bearing
// CR/LF in either Email or Name is rejected before serialisation.
func validateMessageAddresses(msg *mail.Message) error {
	if err := msg.GetFrom().Validate(); err != nil {
		return fmt.Errorf("mail: smtp From: %w", err)
	}
	for _, a := range msg.GetTo() {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("mail: smtp To: %w", err)
		}
	}
	for _, a := range msg.GetCC() {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("mail: smtp Cc: %w", err)
		}
	}
	for _, a := range msg.GetBCC() {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("mail: smtp Bcc: %w", err)
		}
	}
	for _, a := range msg.GetReplyTo() {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("mail: smtp Reply-To: %w", err)
		}
	}
	return nil
}

// sendViaSMTP sends email via SMTP.
//
// When a username is configured, the server is required to advertise STARTTLS
// before PlainAuth is offered. If the encryption mode is "tls" (implicit TLS)
// the connection itself is already encrypted and PlainAuth is safe. Any other
// configuration in the presence of credentials returns ErrPlainAuthRefused to
// prevent credential leakage over cleartext.
func (d *LocalDriver) sendViaSMTP(ctx context.Context, msg *mail.Message) error {
	body := d.buildMessage(msg)

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
		return fmt.Errorf("velocity/mail: no recipients specified")
	}

	from := msg.GetFrom().Email
	if from == "" {
		from = d.fromAddr
	}
	addr := fmt.Sprintf("%s:%s", d.host, d.port)

	// No credentials → anonymous send, no auth concerns.
	if d.username == "" {
		return smtp.SendMail(addr, nil, from, recipients, body)
	}

	// Implicit TLS (SMTPS): entire connection is already encrypted; PlainAuth OK.
	if strings.EqualFold(d.encryption, "tls") || strings.EqualFold(d.encryption, "ssl") {
		return d.sendViaImplicitTLS(ctx, addr, from, recipients, body)
	}

	// Otherwise (plain or starttls): require the server to advertise STARTTLS
	// and upgrade the connection before offering PlainAuth.
	return d.sendViaStartTLS(ctx, addr, from, recipients, body)
}

// sendViaImplicitTLS dials a TLS connection and speaks SMTP on it.
func (d *LocalDriver) sendViaImplicitTLS(ctx context.Context, addr, from string, recipients []string, body []byte) error {
	dialer := &tls.Dialer{Config: &tls.Config{ServerName: d.host, MinVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("velocity/mail: failed to dial smtps: %w", err)
	}
	client, err := smtp.NewClient(conn, d.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("velocity/mail: failed to create smtp client: %w", err)
	}
	defer client.Close()

	auth := smtp.PlainAuth("", d.username, d.password, d.host)
	return d.runSMTP(client, auth, from, recipients, body)
}

// sendViaStartTLS dials plaintext, requires STARTTLS, then runs PlainAuth.
func (d *LocalDriver) sendViaStartTLS(ctx context.Context, addr, from string, recipients []string, body []byte) error {
	dialer := newContextDialer(ctx)
	conn, err := dialer("tcp", addr)
	if err != nil {
		return fmt.Errorf("velocity/mail: failed to dial smtp: %w", err)
	}
	client, err := smtp.NewClient(conn, d.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("velocity/mail: failed to create smtp client: %w", err)
	}
	defer client.Close()

	ok, _ := client.Extension("STARTTLS")
	if !ok {
		return ErrPlainAuthRefused
	}
	if err := client.StartTLS(&tls.Config{ServerName: d.host, MinVersion: tls.VersionTLS12}); err != nil {
		return fmt.Errorf("velocity/mail: starttls failed: %w", err)
	}

	auth := smtp.PlainAuth("", d.username, d.password, d.host)
	return d.runSMTP(client, auth, from, recipients, body)
}

// runSMTP performs AUTH / MAIL / RCPT / DATA / QUIT against an active client.
func (d *LocalDriver) runSMTP(client *smtp.Client, auth smtp.Auth, from string, recipients []string, body []byte) error {
	if ok, _ := client.Extension("AUTH"); ok && auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("velocity/mail: smtp auth failed: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("velocity/mail: mail from failed: %w", err)
	}
	for _, rcpt := range recipients {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("velocity/mail: rcpt to failed: %w", err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("velocity/mail: data failed: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		return fmt.Errorf("velocity/mail: write body failed: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("velocity/mail: close body failed: %w", err)
	}
	return client.Quit()
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
		return fmt.Errorf("velocity/mail: no recipients specified")
	}

	// Execute sendmail
	cmd := exec.CommandContext(ctx, d.sendmail, "-t", "-i")
	cmd.Stdin = bytes.NewReader(body)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("velocity/mail: sendmail failed: %w, output: %s", err, string(output))
	}

	return nil
}

// sanitizeHeader drops every C0 control character (U+0000..U+001F) except
// horizontal tab from a header value. The previous implementation stripped
// only CR/LF, which let NUL and other C0 bytes through — NUL in particular
// can truncate strings in downstream C parsers (e.g. sendmail, libesmtp)
// and enable header-injection vectors a simple CRLF check misses.
// DEL (U+007F) is dropped as well since several older MTAs choke on it.
func sanitizeHeader(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
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

// buildMessage builds the RFC 822 email message.
// Composed from writeHeaders / writeBody / writeAttachments helpers.
func (d *LocalDriver) buildMessage(msg *mail.Message) []byte {
	var buf bytes.Buffer

	d.writeHeaders(&buf, msg)

	attachments := msg.GetAttachments()
	if len(attachments) > 0 {
		boundary := generateBoundary()
		buf.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=%s\r\n\r\n", boundary))

		// Text/HTML part
		buf.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		d.writeBody(&buf, msg)

		d.writeAttachments(&buf, attachments, boundary)

		buf.WriteString(fmt.Sprintf("\r\n--%s--\r\n", boundary))
	} else {
		d.writeBody(&buf, msg)
	}

	return buf.Bytes()
}

// writeHeaders writes the From/To/Cc/Reply-To/Subject/Priority/custom headers
// and the MIME-Version line. Callers are expected to follow up with body
// and (optional) attachment sections.
func (d *LocalDriver) writeHeaders(buf *bytes.Buffer, msg *mail.Message) {
	// From header
	from := msg.GetFrom()
	if from.Email == "" {
		from.Email = d.fromAddr
		from.Name = d.fromName
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

	buf.WriteString("MIME-Version: 1.0\r\n")
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

// writeAttachments writes the base64-encoded attachment parts.
func (d *LocalDriver) writeAttachments(buf *bytes.Buffer, attachments []mail.Attachment, boundary string) {
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
}
