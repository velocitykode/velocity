package postmark

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	netmail "net/mail"
	"strings"
	"sync"
	"time"

	"github.com/velocitykode/velocity/mail"
)

// sanitizeHeader drops every C0 control character (U+0000..U+001F) except
// horizontal tab from a header value. NUL in particular can truncate strings
// in downstream parsers and enable header-injection vectors a simple CRLF
// check misses; DEL (U+007F) is dropped as well since several older MTAs
// choke on it.
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

// sanitizeFilename removes characters that could cause injection in MIME headers.
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, "\"", "")
	name = strings.ReplaceAll(name, "\\", "")
	return name
}

// postmarkErrorPreview caps how many bytes of an unexpected error response
// body we surface in a returned error. The body is redacted from the returned
// message to avoid leaking sensitive Postmark error text to clients.
const postmarkErrorPreview = 256

func init() {
	mail.Drivers().Register("postmark", func(_ context.Context, cfg mail.MailConfig) (mail.Mailer, error) {
		return NewPostmarkDriver(cfg.Postmark, cfg.FromAddress, cfg.FromName)
	})
}

// PostmarkDriver sends emails via Postmark API.
// The token field contains sensitive credentials and must not be logged.
type PostmarkDriver struct {
	token         string // SENSITIVE: do not log
	messageStream string
	fromAddr      string
	fromName      string
	client        *http.Client
	mu            sync.Mutex
}

// String returns a safe representation with credentials redacted.
func (d *PostmarkDriver) String() string {
	return fmt.Sprintf("PostmarkDriver{MessageStream:%s, Token:[REDACTED]}", d.messageStream)
}

// NewPostmarkDriver creates a new Postmark driver from the provided config.
// If MessageStream is non-empty it is validated against the configured
// allowlist (see mail.IsAllowedPostmarkStream).
func NewPostmarkDriver(config mail.PostmarkConfig, fromAddr, fromName string) (*PostmarkDriver, error) {
	if config.Token == "" {
		return nil, fmt.Errorf("velocity/mail: POSTMARK_TOKEN is required for postmark driver")
	}

	messageStream := config.MessageStream
	if messageStream == "" {
		messageStream = "outbound"
	}
	if !mail.IsAllowedPostmarkStream(messageStream) {
		return nil, fmt.Errorf("velocity/mail: postmark message stream %q is not allowed", messageStream)
	}

	return &PostmarkDriver{
		token:         config.Token,
		messageStream: messageStream,
		fromAddr:      fromAddr,
		fromName:      fromName,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// Send sends an email via Postmark API
func (d *PostmarkDriver) Send(ctx context.Context, msg *mail.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Defence in depth: reject any address that carries CR/LF before
	// we serialise. Setter-built messages already pass this; literal-
	// constructed mail.Address values are the failure mode covered here.
	if err := validatePostmarkAddresses(msg); err != nil {
		return err
	}

	payload := d.buildPayload(msg)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("mail: failed to marshal postmark request: %w", err)
	}

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
		return fmt.Errorf("velocity/mail: postmark request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check response. On non-200, we read up to postmarkErrorPreview bytes and
	// attempt JSON decoding; both the raw body and decoded structure are
	// redacted from the returned error — only the status code and Postmark
	// ErrorCode (if present) are surfaced, to avoid leaking response content
	// through to clients via wrapped errors.
	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, postmarkErrorPreview+1)) //nolint:forbidigo // bounded by io.LimitReader above
		if readErr != nil {
			return fmt.Errorf("velocity/mail: postmark api error (status %d): read failed: %w", resp.StatusCode, readErr)
		}
		var errorResp struct {
			ErrorCode int    `json:"ErrorCode"`
			Message   string `json:"Message"`
		}
		if decodeErr := json.Unmarshal(body, &errorResp); decodeErr != nil {
			// Decoder failed — we deliberately do NOT include the raw body in
			// the error to avoid leaking response data.
			return fmt.Errorf("velocity/mail: postmark api error (status %d): response not json: %w", resp.StatusCode, decodeErr)
		}
		if errorResp.ErrorCode != 0 {
			return fmt.Errorf("velocity/mail: postmark api error (status %d, code %d)", resp.StatusCode, errorResp.ErrorCode)
		}
		return fmt.Errorf("velocity/mail: postmark api error (status %d)", resp.StatusCode)
	}

	return nil
}

// buildPayload builds the Postmark API payload.
// The work is delegated to smaller helpers: addFrom / addRecipients /
// addSubjectAndBody / addHeaders / addAttachments.
func (d *PostmarkDriver) buildPayload(msg *mail.Message) map[string]interface{} {
	payload := make(map[string]interface{})

	d.addFrom(payload, msg)
	d.addRecipients(payload, msg)
	d.addSubjectAndBody(payload, msg)
	payload["MessageStream"] = d.messageStream
	d.addHeaders(payload, msg)
	d.addAttachments(payload, msg)

	return payload
}

// validateAddresses runs mail.Address.Validate against every address
// field on the message. This is defence in depth against callers that
// bypass Message setters via mail.Address{} struct-literal construction;
// any CR/LF in either Email or Name surfaces as an error before the
// payload is serialised. The fluent setters already block these via
// validateAddressField at construction time, so this check is a no-op
// on the common path.
func validatePostmarkAddresses(msg *mail.Message) error {
	if err := msg.GetFrom().Validate(); err != nil {
		return fmt.Errorf("mail: postmark From: %w", err)
	}
	for _, a := range msg.GetTo() {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("mail: postmark To: %w", err)
		}
	}
	for _, a := range msg.GetCC() {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("mail: postmark Cc: %w", err)
		}
	}
	for _, a := range msg.GetBCC() {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("mail: postmark Bcc: %w", err)
		}
	}
	for _, a := range msg.GetReplyTo() {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("mail: postmark Reply-To: %w", err)
		}
	}
	return nil
}

// formatPostmarkAddress formats an address for a Postmark address header. The
// display name is passed through net/mail.Address, which applies RFC 2047 /
// RFC 5322 phrase quoting so that grammar specials in the name cannot split
// the header. Name content is also stripped of C0 control bytes via
// sanitizeHeader as a belt-and-braces against callers that bypass the
// Message validators.
func formatPostmarkAddress(name, email string) string {
	clean := sanitizeHeader(name)
	if clean == "" {
		return email
	}
	a := netmail.Address{Name: clean, Address: email}
	return a.String()
}

// addFrom sets the From field, applying driver defaults when unset.
func (d *PostmarkDriver) addFrom(payload map[string]interface{}, msg *mail.Message) {
	from := msg.GetFrom()
	if from.Email == "" {
		from.Email = d.fromAddr
		from.Name = d.fromName
	}
	payload["From"] = formatPostmarkAddress(from.Name, from.Email)
}

// addRecipients sets the To / Cc / Bcc / ReplyTo fields.
func (d *PostmarkDriver) addRecipients(payload map[string]interface{}, msg *mail.Message) {
	if to := msg.GetTo(); len(to) > 0 {
		addrs := make([]string, len(to))
		for i, a := range to {
			addrs[i] = formatPostmarkAddress(a.Name, a.Email)
		}
		if len(addrs) == 1 {
			payload["To"] = addrs[0]
		} else {
			payload["To"] = addrs
		}
	}
	if cc := msg.GetCC(); len(cc) > 0 {
		addrs := make([]string, len(cc))
		for i, a := range cc {
			addrs[i] = formatPostmarkAddress(a.Name, a.Email)
		}
		if len(addrs) == 1 {
			payload["Cc"] = addrs[0]
		} else {
			payload["Cc"] = addrs
		}
	}
	if bcc := msg.GetBCC(); len(bcc) > 0 {
		addrs := make([]string, len(bcc))
		for i, a := range bcc {
			addrs[i] = formatPostmarkAddress(a.Name, a.Email)
		}
		if len(addrs) == 1 {
			payload["Bcc"] = addrs[0]
		} else {
			payload["Bcc"] = addrs
		}
	}
	if reply := msg.GetReplyTo(); len(reply) > 0 {
		payload["ReplyTo"] = formatPostmarkAddress(reply[0].Name, reply[0].Email)
	}
}

// addSubjectAndBody sets Subject, TextBody, and HtmlBody.
func (d *PostmarkDriver) addSubjectAndBody(payload map[string]interface{}, msg *mail.Message) {
	payload["Subject"] = msg.GetSubject()
	if textBody := msg.GetTextBody(); textBody != "" {
		payload["TextBody"] = textBody
	}
	if htmlBody := msg.GetHTMLBody(); htmlBody != "" {
		payload["HtmlBody"] = htmlBody
	}
}

// addHeaders converts custom headers into Postmark's Name/Value slice form,
// sanitising both the key and value for CRLF injection.
func (d *PostmarkDriver) addHeaders(payload map[string]interface{}, msg *mail.Message) {
	hs := msg.GetHeaders()
	if len(hs) == 0 {
		return
	}
	headers := make([]map[string]string, 0, len(hs))
	for key, value := range hs {
		headers = append(headers, map[string]string{
			"Name":  sanitizeHeader(key),
			"Value": sanitizeHeader(value),
		})
	}
	payload["Headers"] = headers
}

// addAttachments adds attachments, base64-encoding content as required.
// The attachment Name is sanitised to guard against filename injection.
func (d *PostmarkDriver) addAttachments(payload map[string]interface{}, msg *mail.Message) {
	attachments := msg.GetAttachments()
	if len(attachments) == 0 {
		return
	}
	postmarkAttachments := make([]map[string]interface{}, len(attachments))
	for i, att := range attachments {
		postmarkAttachments[i] = map[string]interface{}{
			"Name":        sanitizeFilename(att.Name),
			"Content":     base64.StdEncoding.EncodeToString(att.Data),
			"ContentType": sanitizeHeader(att.ContentType),
		}
	}
	payload["Attachments"] = postmarkAttachments
}
