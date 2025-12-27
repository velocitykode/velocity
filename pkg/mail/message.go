package mail

import (
	"bytes"
	"context"
	"html/template"
	"mime"
	"os"
	"path/filepath"
)

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

// AttachFile attaches a file from the filesystem
func (m *Message) AttachFile(path string) *Message {
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}

	contentType := mime.TypeByExtension(filepath.Ext(path))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	m.attachments = append(m.attachments, Attachment{
		Name:        filepath.Base(path),
		Data:        data,
		ContentType: contentType,
	})

	return m
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

// Template renders a template with data and sets it as HTML body
func (m *Message) Template(name string, data interface{}) *Message {
	tmpl, err := template.ParseFiles("templates/" + name + ".html")
	if err != nil {
		panic(err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		panic(err)
	}

	m.htmlBody = buf.String()
	return m
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
