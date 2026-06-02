package mail

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	velmail "github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/notification"
)

// --- Test helpers ---

type testNotifiable struct {
	email string
	id    string
}

func (n *testNotifiable) NotificationRoute(channel string) string {
	switch channel {
	case "mail":
		return n.email
	case "database":
		return n.id
	case "broadcast":
		return "user." + n.id
	default:
		return ""
	}
}

// testMailer is an in-memory mailer for testing.
type testMailer struct {
	mu   sync.Mutex
	sent []*velmail.Message
}

func (m *testMailer) Send(ctx context.Context, msg *velmail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

// simpleMailNotification implements Notification + MailNotification.
type simpleMailNotification struct {
	subject string
}

func (n *simpleMailNotification) Via(notifiable interface{}) []string {
	return []string{"mail"}
}

func (n *simpleMailNotification) ToMail(notifiable interface{}) *notification.MailMessage {
	return notification.NewMailMessage().
		Subject(n.subject).
		Greeting("Hello!").
		Line("This is a test.").
		Action("View", "https://example.com").
		Outro("Thank you.")
}

// customBodyMailNotification sends a notification with custom HTML/text.
type customBodyMailNotification struct{}

func (n *customBodyMailNotification) Via(notifiable interface{}) []string {
	return []string{"mail"}
}

func (n *customBodyMailNotification) ToMail(notifiable interface{}) *notification.MailMessage {
	return notification.NewMailMessage().
		Subject("Custom Body").
		From("noreply@example.com", "App").
		To("override@example.com").
		TextBody("Plain text body").
		HTMLBody("<h1>HTML body</h1>")
}

// nonMailNotification does not implement MailNotification.
type nonMailNotification struct{}

func (n *nonMailNotification) Via(notifiable interface{}) []string {
	return []string{"mail"}
}

// nilMailNotification returns nil from ToMail.
type nilMailNotification struct{}

func (n *nilMailNotification) Via(notifiable interface{}) []string {
	return []string{"mail"}
}

func (n *nilMailNotification) ToMail(notifiable interface{}) *notification.MailMessage {
	return nil
}

// --- Mail Channel Tests ---

func TestMailChannelSend(t *testing.T) {
	mailer := &testMailer{}
	ch := NewMailChannel()
	ch.SetMailer(mailer)

	notifiable := &testNotifiable{email: "user@example.com", id: "1"}
	n := &simpleMailNotification{subject: "Welcome"}

	err := ch.Send(context.Background(), notifiable, n)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mailer.sent) != 1 {
		t.Fatalf("expected 1 sent mail, got %d", len(mailer.sent))
	}

	msg := mailer.sent[0]
	if msg.GetSubject() != "Welcome" {
		t.Errorf("expected subject Welcome, got %s", msg.GetSubject())
	}

	// Should use notifiable route for To
	to := msg.GetTo()
	if len(to) != 1 || to[0].Email != "user@example.com" {
		t.Errorf("expected to [user@example.com], got %v", to)
	}

	// Should have rendered HTML body from greeting/lines/action
	html := msg.GetHTMLBody()
	if !strings.Contains(html, "<h1>Hello!</h1>") {
		t.Error("expected HTML body to contain greeting")
	}
	if !strings.Contains(html, "This is a test.") {
		t.Error("expected HTML body to contain line")
	}
	if !strings.Contains(html, "https://example.com") {
		t.Error("expected HTML body to contain action URL")
	}

	// Should have rendered text body
	text := msg.GetTextBody()
	if !strings.Contains(text, "Hello!") {
		t.Error("expected text body to contain greeting")
	}
	if !strings.Contains(text, "View: https://example.com") {
		t.Error("expected text body to contain action")
	}
}

func TestMailChannelCustomBody(t *testing.T) {
	mailer := &testMailer{}
	ch := NewMailChannel()
	ch.SetMailer(mailer)

	notifiable := &testNotifiable{email: "user@example.com", id: "1"}
	n := &customBodyMailNotification{}

	err := ch.Send(context.Background(), notifiable, n)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msg := mailer.sent[0]

	// Should use explicit To from notification, not notifiable route
	to := msg.GetTo()
	if len(to) != 1 || to[0].Email != "override@example.com" {
		t.Errorf("expected to [override@example.com], got %v", to)
	}

	// Should use custom From
	from := msg.GetFrom()
	if from.Email != "noreply@example.com" {
		t.Errorf("expected from noreply@example.com, got %s", from.Email)
	}

	// Should use custom bodies
	if msg.GetTextBody() != "Plain text body" {
		t.Errorf("expected custom text body, got %s", msg.GetTextBody())
	}
	if msg.GetHTMLBody() != "<h1>HTML body</h1>" {
		t.Errorf("expected custom HTML body, got %s", msg.GetHTMLBody())
	}
}

func TestMailChannelNoMailer(t *testing.T) {
	ch := NewMailChannel() // No mailer set

	notifiable := &testNotifiable{email: "user@example.com", id: "1"}
	n := &simpleMailNotification{subject: "Test"}

	err := ch.Send(context.Background(), notifiable, n)
	if err == nil {
		t.Fatal("expected error when no mailer configured")
	}
}

func TestMailChannelNonMailNotification(t *testing.T) {
	mailer := &testMailer{}
	ch := NewMailChannel()
	ch.SetMailer(mailer)

	notifiable := &testNotifiable{email: "user@example.com", id: "1"}
	n := &nonMailNotification{}

	err := ch.Send(context.Background(), notifiable, n)
	if err == nil {
		t.Fatal("expected error for non-mail notification")
	}
}

func TestMailChannelNilMailMessage(t *testing.T) {
	mailer := &testMailer{}
	ch := NewMailChannel()
	ch.SetMailer(mailer)

	notifiable := &testNotifiable{email: "user@example.com", id: "1"}
	n := &nilMailNotification{}

	err := ch.Send(context.Background(), notifiable, n)
	if err != nil {
		t.Fatalf("expected no error for nil mail message, got %v", err)
	}

	if len(mailer.sent) != 0 {
		t.Error("expected no mail sent for nil message")
	}
}

func TestRenderMailText(t *testing.T) {
	msg := notification.NewMailMessage().
		Greeting("Hi User!").
		Line("Your order has shipped.").
		Line("Tracking: ABC123").
		Action("Track Order", "https://example.com/track").
		Outro("Thanks for your purchase.")

	text := renderMailText(msg)

	if !strings.Contains(text, "Hi User!") {
		t.Error("expected greeting in text")
	}
	if !strings.Contains(text, "Your order has shipped.") {
		t.Error("expected line in text")
	}
	if !strings.Contains(text, "Track Order: https://example.com/track") {
		t.Error("expected action in text")
	}
	if !strings.Contains(text, "Thanks for your purchase.") {
		t.Error("expected outro in text")
	}
}

func TestRenderMailHTML(t *testing.T) {
	msg := notification.NewMailMessage().
		Greeting("Hi!").
		Line("Content line.").
		Action("Click", "https://example.com").
		Outro("Footer line.")

	html := renderMailHTML(msg)

	if !strings.Contains(html, "<h1>Hi!</h1>") {
		t.Error("expected greeting h1 in html")
	}
	if !strings.Contains(html, "<p>Content line.</p>") {
		t.Error("expected line p in html")
	}
	if !strings.Contains(html, `href="https://example.com"`) {
		t.Error("expected action link in html")
	}
	if !strings.Contains(html, "<p>Footer line.</p>") {
		t.Error("expected outro p in html")
	}
}

func TestRenderMailHTMLActionURLScheme(t *testing.T) {
	tests := []struct {
		name          string
		actionURL     string
		wantAnchor    string
		forbiddenHref string
	}{
		{
			name:       "absolute https action URL renders anchor",
			actionURL:  "https://example.com/verify",
			wantAnchor: `href="https://example.com/verify"`,
		},
		{
			name:          "javascript attack URL renders text only",
			actionURL:     "javascript:alert(1)",
			forbiddenHref: `href="javascript:`,
		},
		{
			name:          "data html attack URL renders text only",
			actionURL:     "data:text/html,<script>alert(1)</script>",
			forbiddenHref: `href="data:`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := notification.NewMailMessage().
				Action("Verify Account", tt.actionURL)

			html := renderMailHTML(msg)

			if !strings.Contains(html, "Verify Account") {
				t.Fatal("expected action text in html")
			}
			if tt.wantAnchor != "" && !strings.Contains(html, tt.wantAnchor) {
				t.Fatalf("expected html to contain %q, got %s", tt.wantAnchor, html)
			}
			if tt.forbiddenHref != "" && strings.Contains(html, tt.forbiddenHref) {
				t.Fatalf("expected html not to contain %q, got %s", tt.forbiddenHref, html)
			}
			if tt.forbiddenHref != "" && strings.Contains(html, "<a ") {
				t.Fatalf("expected unsafe action URL to render without an anchor, got %s", html)
			}
		})
	}
}

func TestRenderMailEmptyMessage(t *testing.T) {
	msg := notification.NewMailMessage()

	text := renderMailText(msg)
	html := renderMailHTML(msg)

	if text != "" {
		t.Errorf("expected empty text for empty message, got %s", text)
	}
	if html != "" {
		t.Errorf("expected empty html for empty message, got %s", html)
	}
}

func TestRenderMailHTMLEscapesContent(t *testing.T) {
	msg := notification.NewMailMessage().
		Greeting(`<script>alert("xss")</script>`).
		Line(`User input: <img src=x onerror=alert(1)>`).
		Action(`Click "here"`, `https://example.com/a?b=1&c=2`).
		Outro(`Thanks & goodbye <b>bold</b>`)

	result := renderMailHTML(msg)

	// Greeting should be escaped
	if strings.Contains(result, "<script>") {
		t.Error("greeting was not escaped - XSS vulnerability")
	}
	if !strings.Contains(result, "&lt;script&gt;") {
		t.Error("expected escaped script tag in greeting")
	}

	// Line should be escaped
	if strings.Contains(result, "<img ") {
		t.Error("line was not escaped - XSS vulnerability")
	}

	// Action URL and text should be escaped
	if strings.Contains(result, `"here"`) {
		t.Error("action text was not escaped")
	}
	if !strings.Contains(result, "&#34;here&#34;") {
		t.Error("expected escaped quotes in action text")
	}

	// Outro should be escaped
	if strings.Contains(result, "<b>bold</b>") {
		t.Error("outro was not escaped")
	}
	if !strings.Contains(result, "&amp; goodbye") {
		t.Error("expected escaped ampersand in outro")
	}
}

func TestCreateUnregisteredChannel(t *testing.T) {
	mgr := notification.NewManager()
	_, err := mgr.Channel(fmt.Sprintf("nonexistent-%d", 12345))
	if err == nil {
		t.Error("expected error for unregistered channel")
	}
}
