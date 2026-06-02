package mail

import (
	"context"
	"errors"
	"strings"
	"testing"

	velmail "github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/notification"
)

// maliciousMailNotification emits an action whose Text and URL include
// characters that MUST be HTML-escaped before being interpolated into the
// rendered HTML body. This covers Task 3.
type maliciousMailNotification struct{}

func (n *maliciousMailNotification) Via(interface{}) []string { return []string{"mail"} }

func (n *maliciousMailNotification) ToMail(interface{}) *notification.MailMessage {
	// The action payload is attacker-controlled in many real deployments.
	return notification.NewMailMessage().
		Subject("XSS attempt").
		Line("hello").
		Action(`<script>alert(1)</script>`, `"/><img src=x onerror=alert(1)>`)
}

func TestMailChannel_ActionHTMLEscaped(t *testing.T) {
	m := &testMailer{}
	ch := NewMailChannel()
	ch.SetMailer(m)

	if err := ch.Send(context.Background(), &testNotifiable{email: "u@e.com"}, &maliciousMailNotification{}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(m.sent) != 1 {
		t.Fatalf("expected 1 sent mail, got %d", len(m.sent))
	}

	html := m.sent[0].GetHTMLBody()

	// Raw script tags and javascript-y break-outs must not survive.
	if strings.Contains(html, "<script>") {
		t.Errorf("action.Text was not escaped: %q", html)
	}
	if strings.Contains(html, `"/><img`) {
		t.Errorf("action.URL was not escaped: %q", html)
	}
	if strings.Contains(html, "<a ") || strings.Contains(html, `href=`) {
		t.Errorf("unsafe action.URL should not render as a link: %q", html)
	}

	// The escaped action text should still be present even when the unsafe URL
	// is omitted from the rendered HTML.
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("expected escaped <script>, got %q", html)
	}
}

// crlfHeaderNotification emits a MailMessage whose Header() carries a CRLF
// payload - the canonical SMTP header-injection attack. The notification
// channel must surface this as an ErrInvalidHeader and must NOT deliver the
// message to the mailer.
type crlfHeaderNotification struct{}

func (n *crlfHeaderNotification) Via(interface{}) []string { return []string{"mail"} }

func (n *crlfHeaderNotification) ToMail(interface{}) *notification.MailMessage {
	return notification.NewMailMessage().
		Subject("hi").
		Line("hello").
		Header("X-Injected", "good\r\nBcc: attacker@evil.com")
}

func TestMailChannel_RejectsCRLFInjectedHeaders(t *testing.T) {
	m := &testMailer{}
	ch := NewMailChannel()
	ch.SetMailer(m)

	err := ch.Send(context.Background(), &testNotifiable{email: "u@e.com"}, &crlfHeaderNotification{})
	if err == nil {
		t.Fatal("expected CRLF-injected header to be rejected, got nil")
	}
	if !errors.Is(err, velmail.ErrInvalidHeader) {
		t.Errorf("expected ErrInvalidHeader, got %v", err)
	}
	if len(m.sent) != 0 {
		t.Errorf("mailer must not receive a CRLF-injected message, got %d sends", len(m.sent))
	}
}
