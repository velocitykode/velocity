package mail

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DefaultMaxAttachmentSize is the fallback per-attachment size limit used
// when neither the MailConfig nor the *Message specifies one. 25 MiB matches
// the common sweet spot across SMTP providers (SES 40, SendGrid 30,
// Postmark 10, Mailgun 25) and prevents trivial OOM-via-attachment DoS.
const DefaultMaxAttachmentSize int64 = 25 * 1024 * 1024

// templatePath is the directory where email templates are stored
var templatePath = "resources/views/emails"

// SetTemplatePath configures the directory for email templates
func SetTemplatePath(path string) {
	templatePath = path
}

// defaultMaxAttachmentSize is the package-level fallback used by NewMessage
// when no explicit limit is supplied. NewMailer promotes MailConfig values
// into it at boot so freshly constructed messages inherit the app config.
var (
	defaultMaxAttachmentSize   = DefaultMaxAttachmentSize
	defaultMaxAttachmentSizeMu sync.RWMutex
)

// SetDefaultMaxAttachmentSize replaces the package-level fallback used by
// NewMessage. A zero or negative value resets to DefaultMaxAttachmentSize.
// Safe for concurrent use; intended to be called once during boot.
func SetDefaultMaxAttachmentSize(n int64) {
	defaultMaxAttachmentSizeMu.Lock()
	defer defaultMaxAttachmentSizeMu.Unlock()
	if n <= 0 {
		defaultMaxAttachmentSize = DefaultMaxAttachmentSize
		return
	}
	defaultMaxAttachmentSize = n
}

// GetDefaultMaxAttachmentSize returns the current package-level limit.
func GetDefaultMaxAttachmentSize() int64 {
	defaultMaxAttachmentSizeMu.RLock()
	defer defaultMaxAttachmentSizeMu.RUnlock()
	return defaultMaxAttachmentSize
}

// Message represents an email message
type Message struct {
	from        Address
	to          []Address
	cc          []Address
	bcc         []Address
	replyTo     []Address
	subject     string
	textBody    string
	htmlBody    string
	attachments []Attachment
	headers     map[string]string
	priority    Priority

	// maxAttachmentSize is the per-attachment byte limit enforced by
	// AttachFile and AttachData. Zero means "use the package default".
	maxAttachmentSize int64

	// err is the first error accumulated by a fluent setter (size-limit
	// violation, CRLF injection attempt, ...). It is returned by Err() and
	// surfaced by Manager.Send / checkedMailer.Send before any driver sees
	// the message. Once set, subsequent setters skip their mutation.
	err error
}

// NewMessage creates a new email message. Its attachment size limit is
// initialised from the package-level default (see SetDefaultMaxAttachmentSize).
func NewMessage() *Message {
	return &Message{
		to:                make([]Address, 0),
		cc:                make([]Address, 0),
		bcc:               make([]Address, 0),
		replyTo:           make([]Address, 0),
		attachments:       make([]Attachment, 0),
		headers:           make(map[string]string),
		priority:          NormalPriority,
		maxAttachmentSize: GetDefaultMaxAttachmentSize(),
	}
}

// WithMaxAttachmentSize overrides the per-attachment size limit for this
// message. A zero or negative value resets to the package default. Must be
// called BEFORE AttachFile / AttachData — prior attachments are not
// retroactively re-validated.
func (m *Message) WithMaxAttachmentSize(n int64) *Message {
	if n <= 0 {
		m.maxAttachmentSize = GetDefaultMaxAttachmentSize()
	} else {
		m.maxAttachmentSize = n
	}
	return m
}

// MaxAttachmentSize returns the effective per-attachment size limit.
func (m *Message) MaxAttachmentSize() int64 {
	if m.maxAttachmentSize <= 0 {
		return GetDefaultMaxAttachmentSize()
	}
	return m.maxAttachmentSize
}

// Err returns the first deferred error accumulated by any fluent setter on
// this message (CRLF injection, oversized attachment, ...). It is checked by
// Manager.Send and the checkedMailer wrapper returned from NewMailer, so
// messages with setter errors are rejected before reaching the driver even
// when the caller ignores the individual fluent return values.
func (m *Message) Err() error { return m.err }

// setErr records the first error observed. Subsequent errors are ignored —
// the caller gets the original root cause rather than an avalanche.
func (m *Message) setErr(err error) {
	if m.err == nil && err != nil {
		m.err = err
	}
}

// From sets the sender address
func (m *Message) From(email string, name ...string) *Message {
	if m.err != nil {
		return m
	}
	var n string
	if len(name) > 0 {
		n = name[0]
	}
	if err := validateAddressField("From", email, n); err != nil {
		m.setErr(err)
		return m
	}
	m.from = Address{Email: email, Name: n}
	return m
}

// To adds a recipient
func (m *Message) To(email string, name ...string) *Message {
	if m.err != nil {
		return m
	}
	var n string
	if len(name) > 0 {
		n = name[0]
	}
	if err := validateAddressField("To", email, n); err != nil {
		m.setErr(err)
		return m
	}
	m.to = append(m.to, Address{Email: email, Name: n})
	return m
}

// CC adds a CC recipient
func (m *Message) CC(email string, name ...string) *Message {
	if m.err != nil {
		return m
	}
	var n string
	if len(name) > 0 {
		n = name[0]
	}
	if err := validateAddressField("Cc", email, n); err != nil {
		m.setErr(err)
		return m
	}
	m.cc = append(m.cc, Address{Email: email, Name: n})
	return m
}

// BCC adds a BCC recipient
func (m *Message) BCC(email string, name ...string) *Message {
	if m.err != nil {
		return m
	}
	var n string
	if len(name) > 0 {
		n = name[0]
	}
	if err := validateAddressField("Bcc", email, n); err != nil {
		m.setErr(err)
		return m
	}
	m.bcc = append(m.bcc, Address{Email: email, Name: n})
	return m
}

// ReplyTo sets the reply-to address
func (m *Message) ReplyTo(email string, name ...string) *Message {
	if m.err != nil {
		return m
	}
	var n string
	if len(name) > 0 {
		n = name[0]
	}
	if err := validateAddressField("Reply-To", email, n); err != nil {
		m.setErr(err)
		return m
	}
	m.replyTo = append(m.replyTo, Address{Email: email, Name: n})
	return m
}

// Subject sets the email subject
func (m *Message) Subject(subject string) *Message {
	if m.err != nil {
		return m
	}
	if err := validateHeaderValue("Subject", subject); err != nil {
		m.setErr(err)
		return m
	}
	m.subject = subject
	return m
}

// Body sets the plain text body (convenience method)
func (m *Message) Body(body string) *Message {
	m.textBody = body
	return m
}

// TextBody sets the plain text body
func (m *Message) TextBody(body string) *Message {
	m.textBody = body
	return m
}

// HTMLBody sets the HTML body
func (m *Message) HTMLBody(body string) *Message {
	m.htmlBody = body
	return m
}

// AttachFile attaches a file from the filesystem. Returns an error if the
// file cannot be read, the path contains traversal sequences, or the file
// exceeds the message's MaxAttachmentSize. On size rejection the returned
// error wraps ErrAttachmentTooLarge and the message also carries the error
// via Err() so a missed error return at call time is caught later by Send.
func (m *Message) AttachFile(path string) (*Message, error) {
	if m.err != nil {
		return m, m.err
	}

	// Reject paths containing ".." to prevent directory traversal.
	cleanPath := filepath.Clean(path)
	if strings.Contains(cleanPath, "..") {
		err := fmt.Errorf("mail: invalid attachment path: path traversal not allowed")
		m.setErr(err)
		return m, err
	}

	limit := m.MaxAttachmentSize()

	// Stat first so oversized files are rejected without any read.
	info, err := os.Stat(cleanPath)
	if err != nil {
		wrapped := fmt.Errorf("mail: failed to stat attachment %q: %w", cleanPath, err)
		m.setErr(wrapped)
		return m, wrapped
	}
	if info.Size() > limit {
		wrapped := fmt.Errorf("mail: attachment %q is %d bytes, limit is %d: %w",
			cleanPath, info.Size(), limit, ErrAttachmentTooLarge)
		m.setErr(wrapped)
		return m, wrapped
	}

	f, err := os.Open(cleanPath)
	if err != nil {
		wrapped := fmt.Errorf("mail: failed to open attachment %q: %w", cleanPath, err)
		m.setErr(wrapped)
		return m, wrapped
	}
	defer f.Close()

	// Read at most limit+1 bytes so a file that grew after Stat still trips
	// the size check rather than being silently truncated or consuming
	// unbounded memory.
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		wrapped := fmt.Errorf("mail: failed to read attachment %q: %w", cleanPath, err)
		m.setErr(wrapped)
		return m, wrapped
	}
	if int64(len(data)) > limit {
		wrapped := fmt.Errorf("mail: attachment %q exceeds limit of %d bytes: %w",
			cleanPath, limit, ErrAttachmentTooLarge)
		m.setErr(wrapped)
		return m, wrapped
	}

	contentType := mime.TypeByExtension(filepath.Ext(cleanPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	m.attachments = append(m.attachments, Attachment{
		Name:        filepath.Base(cleanPath),
		Data:        data,
		ContentType: contentType,
	})

	return m, nil
}

// AttachData attaches data from memory. If len(data) exceeds the message's
// MaxAttachmentSize the attachment is dropped and the message carries
// ErrAttachmentTooLarge via Err(), which Send will surface.
func (m *Message) AttachData(data []byte, name, contentType string) *Message {
	if m.err != nil {
		return m
	}
	limit := m.MaxAttachmentSize()
	if int64(len(data)) > limit {
		m.setErr(fmt.Errorf("mail: in-memory attachment %q is %d bytes, limit is %d: %w",
			name, len(data), limit, ErrAttachmentTooLarge))
		return m
	}
	m.attachments = append(m.attachments, Attachment{
		Name:        name,
		Data:        data,
		ContentType: contentType,
	})
	return m
}

// Template renders a template with data and sets it as HTML body.
// Returns an error if the template name is invalid or template processing fails.
func (m *Message) Template(name string, data interface{}) (*Message, error) {
	if m.err != nil {
		return m, m.err
	}
	// Validate template name: reject path separators and traversal sequences.
	if strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
		err := fmt.Errorf("mail: invalid template name %q: must not contain path separators or traversal sequences", name)
		m.setErr(err)
		return m, err
	}

	tmplFile := filepath.Join(templatePath, name+".html")
	// Verify the resolved path stays within templatePath.
	cleanBase := filepath.Clean(templatePath) + string(filepath.Separator)
	cleanFile := filepath.Clean(tmplFile)
	if !strings.HasPrefix(cleanFile, cleanBase) {
		err := fmt.Errorf("mail: template path traversal detected")
		m.setErr(err)
		return m, err
	}

	tmpl, err := template.ParseFiles(cleanFile)
	if err != nil {
		wrapped := fmt.Errorf("mail: failed to parse template %q: %w", name, err)
		m.setErr(wrapped)
		return m, wrapped
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		wrapped := fmt.Errorf("mail: failed to execute template %q: %w", name, err)
		m.setErr(wrapped)
		return m, wrapped
	}

	m.htmlBody = buf.String()
	return m, nil
}

// Header adds a custom header. Both key and value are validated: the key must
// be a legal RFC 5322 header token (no CTLs, ':', or whitespace) and the
// value must not contain CR or LF. Violations are stored as a deferred error
// on the message (see Err) rather than breaking the fluent signature; Send
// rejects the message before it leaves the process.
func (m *Message) Header(key, value string) *Message {
	if m.err != nil {
		return m
	}
	if err := validateHeaderName(key); err != nil {
		m.setErr(err)
		return m
	}
	if err := validateHeaderValue(key, value); err != nil {
		m.setErr(err)
		return m
	}
	m.headers[key] = value
	return m
}

// Priority sets the email priority
func (m *Message) Priority(priority Priority) *Message {
	m.priority = priority
	return m
}

// --- validation helpers (unexported) -----------------------------------------

// containsForbiddenControl reports whether s contains CR, LF, NUL, or any
// other C0 control byte (or DEL) that would enable SMTP header injection or
// split the message body. HTAB (\t) is allowed because it is legal inside
// structured header values.
func containsForbiddenControl(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\r' || c == '\n' || c == 0x00 {
			return true
		}
		if c < 0x20 && c != '\t' {
			return true
		}
		if c == 0x7f {
			return true
		}
	}
	return false
}

// validateHeaderName rejects header names that contain CR, LF, ':' (which
// would terminate the name), whitespace, or any control character. RFC 5322
// §3.2.3 restricts field-name to visible printable ASCII minus colon.
func validateHeaderName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty header name", ErrInvalidHeader)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '\r' || c == '\n' || c == '\t' || c == ':' || c == ' ' {
			return fmt.Errorf("%w: header name %q contains forbidden character", ErrInvalidHeader, name)
		}
		// RFC 5322 token: visible printable ASCII (33..126) minus ':'.
		if c < 33 || c > 126 {
			return fmt.Errorf("%w: header name %q contains non-ASCII or control byte", ErrInvalidHeader, name)
		}
	}
	return nil
}

// validateHeaderValue rejects CR, LF, NUL and other C0 controls in header
// values to block SMTP header injection. We do NOT implement RFC 5322 folding
// (CRLF + WSP) — callers that need long headers should split them into
// separate fields. This is the conservative stance recommended for 1.0.
func validateHeaderValue(field, value string) error {
	if containsForbiddenControl(value) {
		return fmt.Errorf("%w: %s value contains CR/LF or other control characters", ErrInvalidHeader, field)
	}
	return nil
}

// validateAddressField validates the email and display-name components of a
// structured address setter (From/To/Cc/Bcc/ReplyTo). Either side containing
// CR/LF/NUL/etc. is rejected, since both are ultimately serialised into a
// header line by every driver.
func validateAddressField(field, email, name string) error {
	if err := validateHeaderValue(field+" address", email); err != nil {
		return err
	}
	if name != "" {
		if err := validateHeaderValue(field+" name", name); err != nil {
			return err
		}
	}
	return nil
}

// IsErrAttachmentTooLarge is a convenience for errors.Is(err, ErrAttachmentTooLarge).
func IsErrAttachmentTooLarge(err error) bool { return errors.Is(err, ErrAttachmentTooLarge) }

// IsErrInvalidHeader is a convenience for errors.Is(err, ErrInvalidHeader).
func IsErrInvalidHeader(err error) bool { return errors.Is(err, ErrInvalidHeader) }

// Getters for driver access

// GetFrom returns the from address
func (m *Message) GetFrom() Address {
	return m.from
}

// GetTo returns the to addresses
func (m *Message) GetTo() []Address {
	return m.to
}

// GetCC returns the CC addresses
func (m *Message) GetCC() []Address {
	return m.cc
}

// GetBCC returns the BCC addresses
func (m *Message) GetBCC() []Address {
	return m.bcc
}

// GetReplyTo returns the reply-to addresses
func (m *Message) GetReplyTo() []Address {
	return m.replyTo
}

// GetSubject returns the subject
func (m *Message) GetSubject() string {
	return m.subject
}

// GetTextBody returns the plain text body
func (m *Message) GetTextBody() string {
	return m.textBody
}

// GetHTMLBody returns the HTML body
func (m *Message) GetHTMLBody() string {
	return m.htmlBody
}

// GetAttachments returns the attachments
func (m *Message) GetAttachments() []Attachment {
	return m.attachments
}

// GetHeaders returns the custom headers
func (m *Message) GetHeaders() map[string]string {
	return m.headers
}

// GetPriority returns the priority
func (m *Message) GetPriority() Priority {
	return m.priority
}
