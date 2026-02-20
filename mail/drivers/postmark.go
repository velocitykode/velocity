package drivers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/velocitykode/velocity/mail"
)

func init() {
	mail.RegisterDriver("postmark", func() (mail.Mailer, error) {
		return NewPostmarkDriver()
	})
}

// PostmarkDriver sends emails via Postmark API.
// The token field contains sensitive credentials and must not be logged.
type PostmarkDriver struct {
	token         string // SENSITIVE: do not log
	messageStream string
	client        *http.Client
	mu            sync.Mutex
}

// String returns a safe representation with credentials redacted.
func (d *PostmarkDriver) String() string {
	return fmt.Sprintf("PostmarkDriver{MessageStream:%s, Token:[REDACTED]}", d.messageStream)
}

// NewPostmarkDriver creates a new Postmark driver
func NewPostmarkDriver() (*PostmarkDriver, error) {
	token := os.Getenv("POSTMARK_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("mail: POSTMARK_TOKEN is required for postmark driver")
	}

	messageStream := os.Getenv("POSTMARK_MESSAGE_STREAM")
	if messageStream == "" {
		messageStream = "outbound" // Default stream
	}

	return &PostmarkDriver{
		token:         token,
		messageStream: messageStream,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// Send sends an email via Postmark API
func (d *PostmarkDriver) Send(ctx context.Context, msg *mail.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Build Postmark request
	payload := d.buildPayload(msg)

	// Marshal to JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("mail: failed to marshal postmark request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.postmarkapp.com/email", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("mail: failed to create postmark request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Postmark-Server-Token", d.token)

	// Send request
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("mail: postmark request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check response
	if resp.StatusCode != http.StatusOK {
		var errorResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errorResp)
		return fmt.Errorf("mail: postmark API error (status %d): %v", resp.StatusCode, errorResp)
	}

	return nil
}

// buildPayload builds the Postmark API payload
func (d *PostmarkDriver) buildPayload(msg *mail.Message) map[string]interface{} {
	payload := make(map[string]interface{})

	// From
	from := msg.GetFrom()
	if from.Email == "" {
		from.Email = os.Getenv("MAIL_FROM_ADDRESS")
		from.Name = os.Getenv("MAIL_FROM_NAME")
	}
	if from.Name != "" {
		payload["From"] = fmt.Sprintf("%s <%s>", from.Name, from.Email)
	} else {
		payload["From"] = from.Email
	}

	// To
	to := msg.GetTo()
	if len(to) > 0 {
		toAddrs := make([]string, len(to))
		for i, addr := range to {
			if addr.Name != "" {
				toAddrs[i] = fmt.Sprintf("%s <%s>", addr.Name, addr.Email)
			} else {
				toAddrs[i] = addr.Email
			}
		}
		payload["To"] = toAddrs[0] // Postmark accepts single To or comma-separated
		if len(toAddrs) > 1 {
			payload["To"] = toAddrs
		}
	}

	// CC
	cc := msg.GetCC()
	if len(cc) > 0 {
		ccAddrs := make([]string, len(cc))
		for i, addr := range cc {
			if addr.Name != "" {
				ccAddrs[i] = fmt.Sprintf("%s <%s>", addr.Name, addr.Email)
			} else {
				ccAddrs[i] = addr.Email
			}
		}
		payload["Cc"] = ccAddrs[0]
		if len(ccAddrs) > 1 {
			payload["Cc"] = ccAddrs
		}
	}

	// BCC
	bcc := msg.GetBCC()
	if len(bcc) > 0 {
		bccAddrs := make([]string, len(bcc))
		for i, addr := range bcc {
			if addr.Name != "" {
				bccAddrs[i] = fmt.Sprintf("%s <%s>", addr.Name, addr.Email)
			} else {
				bccAddrs[i] = addr.Email
			}
		}
		payload["Bcc"] = bccAddrs[0]
		if len(bccAddrs) > 1 {
			payload["Bcc"] = bccAddrs
		}
	}

	// Reply-To
	replyTo := msg.GetReplyTo()
	if len(replyTo) > 0 {
		if replyTo[0].Name != "" {
			payload["ReplyTo"] = fmt.Sprintf("%s <%s>", replyTo[0].Name, replyTo[0].Email)
		} else {
			payload["ReplyTo"] = replyTo[0].Email
		}
	}

	// Subject
	payload["Subject"] = msg.GetSubject()

	// Body
	textBody := msg.GetTextBody()
	if textBody != "" {
		payload["TextBody"] = textBody
	}

	htmlBody := msg.GetHTMLBody()
	if htmlBody != "" {
		payload["HtmlBody"] = htmlBody
	}

	// Message stream
	payload["MessageStream"] = d.messageStream

	// Headers
	headers := make([]map[string]string, 0)
	for key, value := range msg.GetHeaders() {
		headers = append(headers, map[string]string{
			"Name":  sanitizeHeader(key),
			"Value": sanitizeHeader(value),
		})
	}
	if len(headers) > 0 {
		payload["Headers"] = headers
	}

	// Attachments
	attachments := msg.GetAttachments()
	if len(attachments) > 0 {
		postmarkAttachments := make([]map[string]interface{}, len(attachments))
		for i, att := range attachments {
			postmarkAttachments[i] = map[string]interface{}{
				"Name":        att.Name,
				"Content":     base64.StdEncoding.EncodeToString(att.Data),
				"ContentType": att.ContentType,
			}
		}
		payload["Attachments"] = postmarkAttachments
	}

	return payload
}
