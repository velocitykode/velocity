package channels

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/notification"
)

func init() {
	notification.RegisterChannel("mail", func() (notification.Channel, error) {
		return NewMailChannel(), nil
	})
}

// MailChannel delivers notifications via the mail system.
// It requires a mail.Mailer to be set before sending.
type MailChannel struct {
	mailer mail.Mailer
}

// NewMailChannel creates a new mail notification channel.
func NewMailChannel() *MailChannel {
	return &MailChannel{}
}

// SetMailer sets the mailer used to deliver notifications.
func (c *MailChannel) SetMailer(mailer mail.Mailer) {
	c.mailer = mailer
}

// Send delivers a notification via mail.
func (c *MailChannel) Send(ctx context.Context, notifiable interface{}, n notification.Notification) error {
	mn, ok := n.(notification.MailNotification)
	if !ok {
		return fmt.Errorf("notification: %T does not implement MailNotification", n)
	}

	mailMsg := mn.ToMail(notifiable)
	if mailMsg == nil {
		return nil
	}

	if c.mailer == nil {
		return fmt.Errorf("notification: mail channel has no mailer configured")
	}

	// Build the mail.Message from the notification's MailMessage
	msg := mail.NewMessage()

	// Set from
	from := mailMsg.GetFrom()
	if from.Email != "" {
		msg.From(from.Email, from.Name)
	}

	// Set recipients — use notification-specified To, or fall back to notifiable route
	toAddrs := mailMsg.GetTo()
	if len(toAddrs) > 0 {
		for _, addr := range toAddrs {
			msg.To(addr)
		}
	} else if nr, ok := notifiable.(notification.Notifiable); ok {
		route := nr.NotificationRoute("mail")
		if route != "" {
			msg.To(route)
		}
	}

	// CC
	for _, addr := range mailMsg.GetCC() {
		msg.CC(addr)
	}

	// BCC
	for _, addr := range mailMsg.GetBCC() {
		msg.BCC(addr)
	}

	// Reply-To
	if replyTo := mailMsg.GetReplyTo(); replyTo != "" {
		msg.ReplyTo(replyTo)
	}

	// Subject
	msg.Subject(mailMsg.GetSubject())

	// Body — use custom body if set, otherwise render from greeting/lines/action
	if htmlBody := mailMsg.GetHTMLBody(); htmlBody != "" {
		msg.HTMLBody(htmlBody)
	} else {
		html := renderMailHTML(mailMsg)
		if html != "" {
			msg.HTMLBody(html)
		}
	}

	if textBody := mailMsg.GetTextBody(); textBody != "" {
		msg.TextBody(textBody)
	} else {
		text := renderMailText(mailMsg)
		if text != "" {
			msg.TextBody(text)
		}
	}

	// Attachments
	for _, att := range mailMsg.GetAttachments() {
		msg.AttachData(att.Data, att.Name, att.ContentType)
	}

	// Priority
	msg.Priority(mailMsg.GetPriority())

	// Headers
	for key, value := range mailMsg.GetHeaders() {
		msg.Header(key, value)
	}

	return c.mailer.Send(ctx, msg)
}

// renderMailText renders a plain text body from the structured MailMessage fields.
func renderMailText(m *notification.MailMessage) string {
	var parts []string

	if greeting := m.GetGreeting(); greeting != "" {
		parts = append(parts, greeting)
		parts = append(parts, "")
	}

	parts = append(parts, m.GetLines()...)

	if action := m.GetAction(); action != nil {
		parts = append(parts, "")
		parts = append(parts, fmt.Sprintf("%s: %s", action.Text, action.URL))
		parts = append(parts, "")
	}

	parts = append(parts, m.GetOutro()...)

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, "\n")
}

// renderMailHTML renders a simple HTML body from the structured MailMessage fields.
// All user-supplied content is escaped with html.EscapeString to prevent XSS.
func renderMailHTML(m *notification.MailMessage) string {
	var parts []string

	if greeting := m.GetGreeting(); greeting != "" {
		parts = append(parts, "<h1>"+html.EscapeString(greeting)+"</h1>")
	}

	for _, line := range m.GetLines() {
		parts = append(parts, "<p>"+html.EscapeString(line)+"</p>")
	}

	if action := m.GetAction(); action != nil {
		parts = append(parts, fmt.Sprintf(
			`<p><a href="%s" style="display:inline-block;padding:10px 20px;background:#3490dc;color:#fff;text-decoration:none;border-radius:4px;">%s</a></p>`,
			html.EscapeString(action.URL), html.EscapeString(action.Text),
		))
	}

	for _, line := range m.GetOutro() {
		parts = append(parts, "<p>"+html.EscapeString(line)+"</p>")
	}

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, "\n")
}
