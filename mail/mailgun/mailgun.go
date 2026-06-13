package mailgun

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	netmail "net/mail"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/velocitykode/velocity/mail"
)

// mailgunErrorPreview caps how many bytes of an unexpected error response
// body we read from Mailgun for JSON decoding. The body is redacted from
// the returned message to avoid leaking sensitive Mailgun error text to
// clients.
const mailgunErrorPreview = 256

// formatAddress formats an Address for an email header. The display name is
// passed through net/mail.Address, which applies RFC 2047 / RFC 5322 phrase
// quoting so that grammar specials in the name cannot split the header. Name
// content is also stripped of C0 control bytes via sanitizeHeader as a
// belt-and-braces against callers that bypass the Message validators.
func formatAddress(name, email string) string {
	clean := mail.SanitizeHeader(name)
	if clean == "" {
		return email
	}
	a := netmail.Address{Name: clean, Address: email}
	return a.String()
}

func init() {
	mail.Drivers().Register("mailgun", func(_ context.Context, cfg mail.MailConfig) (mail.Mailer, error) {
		return NewMailgunDriver(cfg.Mailgun, cfg.FromAddress, cfg.FromName)
	})
}

// MailgunDriver sends emails via Mailgun API.
// The apiKey and webhookSigningKey fields contain sensitive credentials and must not be logged.
type MailgunDriver struct {
	domain            string
	apiKey            string // SENSITIVE: do not log
	endpoint          string
	webhookSigningKey string // SENSITIVE: do not log
	fromAddr          string
	fromName          string
	client            *http.Client
	mu                sync.Mutex
}

// String returns a safe representation with credentials redacted.
func (d *MailgunDriver) String() string {
	return fmt.Sprintf("MailgunDriver{Domain:%s, Endpoint:%s, APIKey:[REDACTED]}", d.domain, d.endpoint)
}

// NewMailgunDriver creates a new Mailgun driver from the provided config.
//
// Endpoints that use the http:// scheme are rejected: Mailgun credentials
// must never be transmitted over cleartext.
func NewMailgunDriver(config mail.MailgunConfig, fromAddr, fromName string) (*MailgunDriver, error) {
	if config.Domain == "" {
		return nil, fmt.Errorf("velocity/mail: MAILGUN_DOMAIN is required for mailgun driver")
	}

	if config.Secret == "" {
		return nil, fmt.Errorf("velocity/mail: MAILGUN_SECRET is required for mailgun driver")
	}

	endpoint := config.Endpoint
	if endpoint == "" {
		endpoint = "https://api.mailgun.net/v3"
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("velocity/mail: mailgun endpoint is invalid: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, fmt.Errorf("velocity/mail: mailgun endpoint must use https, got %q", u.Scheme)
	}

	return &MailgunDriver{
		domain:            config.Domain,
		apiKey:            config.Secret,
		endpoint:          endpoint,
		webhookSigningKey: config.WebhookSigningKey,
		fromAddr:          fromAddr,
		fromName:          fromName,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// Send sends an email via Mailgun API
func (d *MailgunDriver) Send(ctx context.Context, msg *mail.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	if err := d.addFields(writer, msg); err != nil {
		return fmt.Errorf("mail: failed to build mailgun request: %w", err)
	}

	if err := d.addAttachments(writer, msg); err != nil {
		return fmt.Errorf("mail: failed to add attachments: %w", err)
	}

	writer.Close()

	url := fmt.Sprintf("%s/%s/messages", d.endpoint, d.domain)
	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return fmt.Errorf("mail: failed to create mailgun request: %w", err)
	}

	req.SetBasicAuth("api", d.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("mail: mailgun request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check response. On non-200, we drain up to mailgunErrorPreview bytes
	// (keeping the connection reusable without reading an unbounded body) and
	// return a status-only error: the response body is never inspected or
	// surfaced, to avoid leaking response content through to clients via
	// wrapped errors. Mailgun's error shape ({"message":"..."}) carries no
	// numeric error code, so unlike the Postmark driver there is no safe
	// field to surface; even a JSON decode error string could echo body
	// bytes, so we do not attempt decoding at all.
	if resp.StatusCode != http.StatusOK {
		if _, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, mailgunErrorPreview+1)); readErr != nil {
			return fmt.Errorf("mail: mailgun API error (status %d): read failed: %w", resp.StatusCode, readErr)
		}
		return fmt.Errorf("mail: mailgun API error (status %d)", resp.StatusCode)
	}

	return nil
}

// addFields adds form fields to the multipart writer.
// The work is delegated to smaller helpers for recipients, body, priority, and custom headers.
func (d *MailgunDriver) addFields(writer *multipart.Writer, msg *mail.Message) error {
	if err := d.writeFromField(writer, msg); err != nil {
		return err
	}
	if err := d.writeRecipientFields(writer, msg); err != nil {
		return err
	}
	if err := d.writeSubjectAndBody(writer, msg); err != nil {
		return err
	}
	if err := d.writePriority(writer, msg); err != nil {
		return err
	}
	return d.writeCustomHeaders(writer, msg)
}

// writeFromField writes the sender, applying driver defaults when unset.
// The address is validated via mail.Address.Validate before serialisation
// as defence in depth against callers that bypass Message setters via
// struct-literal Address construction.
func (d *MailgunDriver) writeFromField(writer *multipart.Writer, msg *mail.Message) error {
	from := msg.GetFrom()
	if from.Email == "" {
		from.Email = d.fromAddr
		from.Name = d.fromName
	}
	if err := from.Validate(); err != nil {
		return fmt.Errorf("mail: mailgun From: %w", err)
	}
	return writer.WriteField("from", formatAddress(from.Name, from.Email))
}

// writeRecipientFields writes To / Cc / Bcc / Reply-To fields.
// addr.Name values are sanitised to strip CRLF header-injection payloads.
// Each address is also validated via mail.Address.Validate before
// serialisation, blocking any literal-constructed Address that carries
// CR/LF in either field.
func (d *MailgunDriver) writeRecipientFields(writer *multipart.Writer, msg *mail.Message) error {
	for _, addr := range msg.GetTo() {
		if err := addr.Validate(); err != nil {
			return fmt.Errorf("mail: mailgun To: %w", err)
		}
		if err := writer.WriteField("to", formatAddress(addr.Name, addr.Email)); err != nil {
			return err
		}
	}
	for _, addr := range msg.GetCC() {
		if err := addr.Validate(); err != nil {
			return fmt.Errorf("mail: mailgun Cc: %w", err)
		}
		if err := writer.WriteField("cc", formatAddress(addr.Name, addr.Email)); err != nil {
			return err
		}
	}
	for _, addr := range msg.GetBCC() {
		if err := addr.Validate(); err != nil {
			return fmt.Errorf("mail: mailgun Bcc: %w", err)
		}
		if err := writer.WriteField("bcc", formatAddress(addr.Name, addr.Email)); err != nil {
			return err
		}
	}
	replyTo := msg.GetReplyTo()
	if len(replyTo) > 0 {
		if err := replyTo[0].Validate(); err != nil {
			return fmt.Errorf("mail: mailgun Reply-To: %w", err)
		}
		if err := writer.WriteField("h:Reply-To", formatAddress(replyTo[0].Name, replyTo[0].Email)); err != nil {
			return err
		}
	}
	return nil
}

// writeSubjectAndBody writes subject and both text/html body fields.
func (d *MailgunDriver) writeSubjectAndBody(writer *multipart.Writer, msg *mail.Message) error {
	if err := writer.WriteField("subject", msg.GetSubject()); err != nil {
		return err
	}
	if textBody := msg.GetTextBody(); textBody != "" {
		if err := writer.WriteField("text", textBody); err != nil {
			return err
		}
	}
	if htmlBody := msg.GetHTMLBody(); htmlBody != "" {
		if err := writer.WriteField("html", htmlBody); err != nil {
			return err
		}
	}
	return nil
}

// writePriority writes Mailgun's o:priority field when the message is not normal.
func (d *MailgunDriver) writePriority(writer *multipart.Writer, msg *mail.Message) error {
	switch msg.GetPriority() {
	case mail.HighPriority:
		return writer.WriteField("o:priority", "high")
	case mail.LowPriority:
		return writer.WriteField("o:priority", "low")
	}
	return nil
}

// writeCustomHeaders writes user-supplied headers with CRLF sanitisation.
func (d *MailgunDriver) writeCustomHeaders(writer *multipart.Writer, msg *mail.Message) error {
	for key, value := range msg.GetHeaders() {
		if err := writer.WriteField(fmt.Sprintf("h:%s", mail.SanitizeHeader(key)), mail.SanitizeHeader(value)); err != nil {
			return err
		}
	}
	return nil
}

// addAttachments adds file attachments to the multipart writer
func (d *MailgunDriver) addAttachments(writer *multipart.Writer, msg *mail.Message) error {
	attachments := msg.GetAttachments()
	for _, att := range attachments {
		// Create file field. CreateFormFile's escapeQuotes handles quotes and
		// backslashes but not CR/LF, so a newline in att.Name would otherwise
		// reach the Content-Disposition line; sanitise it first.
		part, err := writer.CreateFormFile("attachment", mail.SanitizeFilename(att.Name))
		if err != nil {
			return err
		}

		// Write attachment data
		if _, err := part.Write(att.Data); err != nil {
			return err
		}
	}

	return nil
}

// ParseWebhook parses a Mailgun webhook event (for future use)
func (d *MailgunDriver) ParseWebhook(body []byte) (map[string]interface{}, error) {
	var event map[string]interface{}
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, err
	}
	return event, nil
}

// VerifyWebhookSignature verifies a Mailgun webhook signature using HMAC-SHA256.
// The signature is computed as HMAC-SHA256(webhookSigningKey, timestamp+token).
func (d *MailgunDriver) VerifyWebhookSignature(timestamp, token, signature string) bool {
	if d.webhookSigningKey == "" || timestamp == "" || token == "" || signature == "" {
		return false
	}

	mac := hmac.New(sha256.New, []byte(d.webhookSigningKey))
	mac.Write([]byte(timestamp + token))
	expected := hex.EncodeToString(mac.Sum(nil))

	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}
