package mail

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

// templatePath is the directory where email templates are stored
var templatePath = "resources/views/emails"

// SetTemplatePath configures the directory for email templates
func SetTemplatePath(path string) {
	templatePath = path
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
}

// NewMessage creates a new email message
func NewMessage() *Message {
	return &Message{
		to:          make([]Address, 0),
		cc:          make([]Address, 0),
		bcc:         make([]Address, 0),
		replyTo:     make([]Address, 0),
		attachments: make([]Attachment, 0),
		headers:     make(map[string]string),
		priority:    NormalPriority,
	}
}

// From sets the sender address
func (m *Message) From(email string, name ...string) *Message {
	m.from = Address{Email: email}
	if len(name) > 0 {
		m.from.Name = name[0]
	}
	return m
}

// To adds a recipient
func (m *Message) To(email string, name ...string) *Message {
	addr := Address{Email: email}
	if len(name) > 0 {
		addr.Name = name[0]
	}
	m.to = append(m.to, addr)
	return m
}

// CC adds a CC recipient
func (m *Message) CC(email string, name ...string) *Message {
	addr := Address{Email: email}
	if len(name) > 0 {
		addr.Name = name[0]
	}
	m.cc = append(m.cc, addr)
	return m
}

// BCC adds a BCC recipient
func (m *Message) BCC(email string, name ...string) *Message {
	addr := Address{Email: email}
	if len(name) > 0 {
		addr.Name = name[0]
	}
	m.bcc = append(m.bcc, addr)
	return m
}

// ReplyTo sets the reply-to address
func (m *Message) ReplyTo(email string, name ...string) *Message {
	addr := Address{Email: email}
	if len(name) > 0 {
		addr.Name = name[0]
	}
	m.replyTo = append(m.replyTo, addr)
	return m
}

// Subject sets the email subject
func (m *Message) Subject(subject string) *Message {
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

// AttachFile attaches a file from the filesystem.
// Returns an error if the file cannot be read or the path contains traversal sequences.
func (m *Message) AttachFile(path string) (*Message, error) {
	// Reject paths containing ".." to prevent directory traversal
	cleanPath := filepath.Clean(path)
	if strings.Contains(cleanPath, "..") {
		return m, fmt.Errorf("mail: invalid attachment path: path traversal not allowed")
	}

	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return m, fmt.Errorf("mail: failed to read attachment %q: %w", cleanPath, err)
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

// AttachData attaches data from memory
func (m *Message) AttachData(data []byte, name, contentType string) *Message {
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
	// Validate template name: reject path separators and traversal sequences
	if strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
		return m, fmt.Errorf("mail: invalid template name %q: must not contain path separators or traversal sequences", name)
	}

	tmplFile := filepath.Join(templatePath, name+".html")
	// Verify the resolved path stays within templatePath
	cleanBase := filepath.Clean(templatePath) + string(filepath.Separator)
	cleanFile := filepath.Clean(tmplFile)
	if !strings.HasPrefix(cleanFile, cleanBase) {
		return m, fmt.Errorf("mail: template path traversal detected")
	}

	tmpl, err := template.ParseFiles(cleanFile)
	if err != nil {
		return m, fmt.Errorf("mail: failed to parse template %q: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return m, fmt.Errorf("mail: failed to execute template %q: %w", name, err)
	}

	m.htmlBody = buf.String()
	return m, nil
}

// Header adds a custom header
func (m *Message) Header(key, value string) *Message {
	m.headers[key] = value
	return m
}

// Priority sets the email priority
func (m *Message) Priority(priority Priority) *Message {
	m.priority = priority
	return m
}

// Send sends the message using the default mailer
func (m *Message) Send() error {
	return Send(context.Background(), m)
}

// SendWithContext sends the message using the default mailer with context
func (m *Message) SendWithContext(ctx context.Context) error {
	return Send(ctx, m)
}

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
