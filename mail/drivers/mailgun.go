package drivers

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
	"os"
	"sync"
	"time"

	"github.com/velocitykode/velocity/mail"
)

func init() {
	mail.RegisterDriver("mailgun", func() (mail.Mailer, error) {
		return NewMailgunDriver()
	})
}

// MailgunDriver sends emails via Mailgun API.
// The apiKey and webhookSigningKey fields contain sensitive credentials and must not be logged.
type MailgunDriver struct {
	domain            string
	apiKey            string // SENSITIVE: do not log
	endpoint          string
	webhookSigningKey string // SENSITIVE: do not log
	client            *http.Client
	mu                sync.Mutex
}

// String returns a safe representation with credentials redacted.
func (d *MailgunDriver) String() string {
	return fmt.Sprintf("MailgunDriver{Domain:%s, Endpoint:%s, APIKey:[REDACTED]}", d.domain, d.endpoint)
}

// NewMailgunDriver creates a new Mailgun driver
func NewMailgunDriver() (*MailgunDriver, error) {
	domain := os.Getenv("MAILGUN_DOMAIN")
	if domain == "" {
		return nil, fmt.Errorf("mail: MAILGUN_DOMAIN is required for mailgun driver")
	}

	apiKey := os.Getenv("MAILGUN_SECRET")
	if apiKey == "" {
		return nil, fmt.Errorf("mail: MAILGUN_SECRET is required for mailgun driver")
	}

	endpoint := os.Getenv("MAILGUN_ENDPOINT")
	if endpoint == "" {
		endpoint = "https://api.mailgun.net/v3"
	}

	webhookSigningKey := os.Getenv("MAILGUN_WEBHOOK_SIGNING_KEY")

	return &MailgunDriver{
		domain:            domain,
		apiKey:            apiKey,
		endpoint:          endpoint,
		webhookSigningKey: webhookSigningKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// Send sends an email via Mailgun API
func (d *MailgunDriver) Send(ctx context.Context, msg *mail.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Build multipart form data
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add fields
	if err := d.addFields(writer, msg); err != nil {
		return fmt.Errorf("mail: failed to build mailgun request: %w", err)
	}

	// Add attachments
	if err := d.addAttachments(writer, msg); err != nil {
		return fmt.Errorf("mail: failed to add attachments: %w", err)
	}

	writer.Close()

	// Create HTTP request
	url := fmt.Sprintf("%s/%s/messages", d.endpoint, d.domain)
	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return fmt.Errorf("mail: failed to create mailgun request: %w", err)
	}

	req.SetBasicAuth("api", d.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Send request
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("mail: mailgun request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check response
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("mail: mailgun API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// addFields adds form fields to the multipart writer
func (d *MailgunDriver) addFields(writer *multipart.Writer, msg *mail.Message) error {
	// From
	from := msg.GetFrom()
	if from.Email == "" {
		from.Email = os.Getenv("MAIL_FROM_ADDRESS")
		from.Name = os.Getenv("MAIL_FROM_NAME")
	}
	if from.Name != "" {
		writer.WriteField("from", fmt.Sprintf("%s <%s>", from.Name, from.Email))
	} else {
		writer.WriteField("from", from.Email)
	}

	// To
	to := msg.GetTo()
	for _, addr := range to {
		if addr.Name != "" {
			writer.WriteField("to", fmt.Sprintf("%s <%s>", addr.Name, addr.Email))
		} else {
			writer.WriteField("to", addr.Email)
		}
	}

	// CC
	cc := msg.GetCC()
	for _, addr := range cc {
		if addr.Name != "" {
			writer.WriteField("cc", fmt.Sprintf("%s <%s>", addr.Name, addr.Email))
		} else {
			writer.WriteField("cc", addr.Email)
		}
	}

	// BCC
	bcc := msg.GetBCC()
	for _, addr := range bcc {
		if addr.Name != "" {
			writer.WriteField("bcc", fmt.Sprintf("%s <%s>", addr.Name, addr.Email))
		} else {
			writer.WriteField("bcc", addr.Email)
		}
	}

	// Reply-To
	replyTo := msg.GetReplyTo()
	if len(replyTo) > 0 {
		if replyTo[0].Name != "" {
			writer.WriteField("h:Reply-To", fmt.Sprintf("%s <%s>", replyTo[0].Name, replyTo[0].Email))
		} else {
			writer.WriteField("h:Reply-To", replyTo[0].Email)
		}
	}

	// Subject
	writer.WriteField("subject", msg.GetSubject())

	// Body
	textBody := msg.GetTextBody()
	if textBody != "" {
		writer.WriteField("text", textBody)
	}

	htmlBody := msg.GetHTMLBody()
	if htmlBody != "" {
		writer.WriteField("html", htmlBody)
	}

	// Priority
	switch msg.GetPriority() {
	case mail.HighPriority:
		writer.WriteField("o:priority", "high")
	case mail.LowPriority:
		writer.WriteField("o:priority", "low")
	}

	// Custom headers
	for key, value := range msg.GetHeaders() {
		writer.WriteField(fmt.Sprintf("h:%s", sanitizeHeader(key)), sanitizeHeader(value))
	}

	return nil
}

// addAttachments adds file attachments to the multipart writer
func (d *MailgunDriver) addAttachments(writer *multipart.Writer, msg *mail.Message) error {
	attachments := msg.GetAttachments()
	for _, att := range attachments {
		// Create file field
		part, err := writer.CreateFormFile("attachment", att.Name)
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
