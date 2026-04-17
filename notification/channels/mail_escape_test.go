package channels

import (
	"context"
	"strings"
	"testing"

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

	// Their escaped forms should be present.
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("expected escaped <script>, got %q", html)
	}
	if !strings.Contains(html, "&#34;/&gt;&lt;img") {
		t.Errorf("expected escaped break-out chars, got %q", html)
	}
}
