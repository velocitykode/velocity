// Package notification provides a unified notification system for sending
// notifications across multiple channels (mail, database, broadcast, Slack).
//
// Notifications implement the Notification interface and optionally implement
// channel-specific interfaces (MailNotification, DatabaseNotification, etc.)
// to define how they are rendered for each delivery channel.
//
// Recipients implement the Notifiable interface to provide routing information
// (e.g., email address, user ID) for each channel.
package notification

import (
	"context"

	"github.com/velocitykode/velocity/mail"
)

// Notification defines a notification that can be sent through one or more channels.
type Notification interface {
	// Via returns the channel names this notification should be delivered through.
	// Channel names correspond to registered Channel drivers (e.g., "mail", "database", "broadcast", "slack").
	Via(notifiable interface{}) []string
}

// Notifiable represents an entity that can receive notifications.
// Typically implemented by a User model.
type Notifiable interface {
	// NotificationRoute returns the routing value for the given channel.
	// For example, "mail" returns an email address, "database" returns a user ID,
	// "slack" returns a Slack webhook URL or channel ID.
	NotificationRoute(channel string) string
}

// Channel is a notification delivery mechanism.
// Each channel knows how to deliver a notification through its transport (mail, DB, etc.).
type Channel interface {
	// Send delivers the notification to the notifiable via this channel.
	Send(ctx context.Context, notifiable interface{}, notification Notification) error
}

// MailNotification is implemented by notifications that can be sent via mail.
type MailNotification interface {
	// ToMail builds the mail message for this notification.
	ToMail(notifiable interface{}) *MailMessage
}

// MailMessage represents an email built from a notification.
type MailMessage struct {
	from        mail.Address
	to          []string
	cc          []string
	bcc         []string
	replyTo     string
	subject     string
	greeting    string
	lines       []string
	action      *MailAction
	outro       []string
	textBody    string
	htmlBody    string
	attachments []mail.Attachment
	priority    mail.Priority
	headers     map[string]string
}

// MailAction represents a call-to-action button in a mail notification.
type MailAction struct {
	Text string
	URL  string
}

// NewMailMessage creates a new MailMessage with sensible defaults.
func NewMailMessage() *MailMessage {
	return &MailMessage{
		priority: mail.NormalPriority,
		headers:  make(map[string]string),
	}
}

// From sets the sender address. Invalid email addresses (CR/LF
// injection payloads, unparseable addr-specs) are silently ignored
// rather than surfaced via a fluent error: the underlying mail driver
// applies the same Validate check at serialisation time and will reject
// the whole send with a descriptive error. The setter routes through
// mail.NewAddress so the in-memory state of the MailMessage never holds
// a header-split payload on the happy path.
func (m *MailMessage) From(email string, name ...string) *MailMessage {
	addr, err := mail.NewAddress(email, name...)
	if err != nil {
		// Leave m.from zero so the eventual MailChannel driver call
		// surfaces "missing from" rather than smuggling a CRLF payload.
		// We deliberately do not panic here because Notifiable.From is
		// often called from notification constructors that do not
		// return errors.
		return m
	}
	m.from = addr
	return m
}

// To adds recipient email addresses.
func (m *MailMessage) To(emails ...string) *MailMessage {
	m.to = append(m.to, emails...)
	return m
}

// CC adds CC recipient email addresses.
func (m *MailMessage) CC(emails ...string) *MailMessage {
	m.cc = append(m.cc, emails...)
	return m
}

// BCC adds BCC recipient email addresses.
func (m *MailMessage) BCC(emails ...string) *MailMessage {
	m.bcc = append(m.bcc, emails...)
	return m
}

// ReplyTo sets the reply-to address.
func (m *MailMessage) ReplyTo(email string) *MailMessage {
	m.replyTo = email
	return m
}

// Subject sets the email subject.
func (m *MailMessage) Subject(subject string) *MailMessage {
	m.subject = subject
	return m
}

// Greeting sets the greeting line (e.g., "Hello John!").
func (m *MailMessage) Greeting(greeting string) *MailMessage {
	m.greeting = greeting
	return m
}

// Line adds a line of text to the notification body.
func (m *MailMessage) Line(line string) *MailMessage {
	m.lines = append(m.lines, line)
	return m
}

// Action adds a call-to-action button.
func (m *MailMessage) Action(text, url string) *MailMessage {
	m.action = &MailAction{Text: text, URL: url}
	return m
}

// Outro adds a line of text after the action.
func (m *MailMessage) Outro(line string) *MailMessage {
	m.outro = append(m.outro, line)
	return m
}

// TextBody sets a custom plain text body (overrides greeting/lines/action rendering).
func (m *MailMessage) TextBody(body string) *MailMessage {
	m.textBody = body
	return m
}

// HTMLBody sets a custom HTML body (overrides greeting/lines/action rendering).
func (m *MailMessage) HTMLBody(body string) *MailMessage {
	m.htmlBody = body
	return m
}

// AttachData attaches data from memory.
func (m *MailMessage) AttachData(data []byte, name, contentType string) *MailMessage {
	m.attachments = append(m.attachments, mail.Attachment{
		Name:        name,
		Data:        data,
		ContentType: contentType,
	})
	return m
}

// Priority sets the email priority.
func (m *MailMessage) Priority(priority mail.Priority) *MailMessage {
	m.priority = priority
	return m
}

// Header adds a custom email header.
func (m *MailMessage) Header(key, value string) *MailMessage {
	m.headers[key] = value
	return m
}

// Getters for channel driver access.

func (m *MailMessage) GetFrom() mail.Address             { return m.from }
func (m *MailMessage) GetTo() []string                   { return m.to }
func (m *MailMessage) GetCC() []string                   { return m.cc }
func (m *MailMessage) GetBCC() []string                  { return m.bcc }
func (m *MailMessage) GetReplyTo() string                { return m.replyTo }
func (m *MailMessage) GetSubject() string                { return m.subject }
func (m *MailMessage) GetGreeting() string               { return m.greeting }
func (m *MailMessage) GetLines() []string                { return m.lines }
func (m *MailMessage) GetAction() *MailAction            { return m.action }
func (m *MailMessage) GetOutro() []string                { return m.outro }
func (m *MailMessage) GetTextBody() string               { return m.textBody }
func (m *MailMessage) GetHTMLBody() string               { return m.htmlBody }
func (m *MailMessage) GetAttachments() []mail.Attachment { return m.attachments }
func (m *MailMessage) GetPriority() mail.Priority        { return m.priority }
func (m *MailMessage) GetHeaders() map[string]string     { return m.headers }

// DatabaseNotification is implemented by notifications that can be stored in a database.
type DatabaseNotification interface {
	// ToDatabase returns the data to store for this notification.
	ToDatabase(notifiable interface{}) *DatabaseMessage
}

// DatabaseMessage represents a notification stored in the database.
type DatabaseMessage struct {
	// Type identifies the notification (e.g., "App.Notifications.InvoicePaid").
	Type string
	// Data holds the notification payload as a map.
	Data map[string]interface{}
}

// NewDatabaseMessage creates a new DatabaseMessage.
func NewDatabaseMessage(notificationType string) *DatabaseMessage {
	return &DatabaseMessage{
		Type: notificationType,
		Data: make(map[string]interface{}),
	}
}

// Set adds a key-value pair to the notification data.
func (m *DatabaseMessage) Set(key string, value interface{}) *DatabaseMessage {
	m.Data[key] = value
	return m
}

// BroadcastNotification is implemented by notifications that should be broadcast in real time.
type BroadcastNotification interface {
	// ToBroadcast returns the data to broadcast for this notification.
	ToBroadcast(notifiable interface{}) *BroadcastMessage
}

// BroadcastMessage represents a notification to broadcast via WebSocket/real-time channels.
type BroadcastMessage struct {
	// Channels to broadcast on (e.g., ["private-user.1"]).
	Channels []string
	// Event name for the broadcast.
	Event string
	// Data payload.
	Data map[string]interface{}
}

// NewBroadcastMessage creates a new BroadcastMessage.
func NewBroadcastMessage(event string) *BroadcastMessage {
	return &BroadcastMessage{
		Event: event,
		Data:  make(map[string]interface{}),
	}
}

// On sets the channels to broadcast on.
func (m *BroadcastMessage) On(channels ...string) *BroadcastMessage {
	m.Channels = channels
	return m
}

// Set adds a key-value pair to the broadcast data.
func (m *BroadcastMessage) Set(key string, value interface{}) *BroadcastMessage {
	m.Data[key] = value
	return m
}

// SlackNotification is implemented by notifications that can be sent to Slack.
type SlackNotification interface {
	// ToSlack builds the Slack message for this notification.
	ToSlack(notifiable interface{}) *SlackMessage
}

// SlackMessage represents a Slack notification.
type SlackMessage struct {
	Channel     string
	Text        string
	Username    string
	IconEmoji   string
	IconURL     string
	Attachments []SlackAttachment
}

// SlackAttachment represents a Slack message attachment.
type SlackAttachment struct {
	Color     string
	Title     string
	TitleLink string
	Text      string
	Fields    []SlackField
	Footer    string
	Timestamp int64
}

// SlackField represents a field in a Slack attachment.
type SlackField struct {
	Title string
	Value string
	Short bool
}

// NewSlackMessage creates a new SlackMessage.
func NewSlackMessage() *SlackMessage {
	return &SlackMessage{}
}

// To sets the Slack channel.
func (m *SlackMessage) To(channel string) *SlackMessage {
	m.Channel = channel
	return m
}

// Content sets the message text.
func (m *SlackMessage) Content(text string) *SlackMessage {
	m.Text = text
	return m
}

// AsUser sets the bot username.
func (m *SlackMessage) AsUser(username string) *SlackMessage {
	m.Username = username
	return m
}

// WithIcon sets the icon emoji (e.g., ":bell:").
func (m *SlackMessage) WithIcon(emoji string) *SlackMessage {
	m.IconEmoji = emoji
	return m
}

// WithIconURL sets the icon URL.
func (m *SlackMessage) WithIconURL(url string) *SlackMessage {
	m.IconURL = url
	return m
}

// Attachment adds a Slack attachment.
func (m *SlackMessage) Attachment(fn func(*SlackAttachment)) *SlackMessage {
	att := &SlackAttachment{}
	fn(att)
	m.Attachments = append(m.Attachments, *att)
	return m
}

// Field adds a field to a SlackAttachment.
func (a *SlackAttachment) Field(title, value string, short ...bool) *SlackAttachment {
	f := SlackField{Title: title, Value: value}
	if len(short) > 0 {
		f.Short = short[0]
	}
	a.Fields = append(a.Fields, f)
	return a
}
