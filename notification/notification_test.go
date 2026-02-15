package notification

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/mail"
)

// --- Test helpers ---

// testNotifiable implements Notifiable for testing.
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

// testNotification implements Notification and MailNotification.
type testNotification struct {
	subject  string
	channels []string
}

func (n *testNotification) Via(notifiable interface{}) []string {
	return n.channels
}

func (n *testNotification) ToMail(notifiable interface{}) *MailMessage {
	return NewMailMessage().
		Subject(n.subject).
		Greeting("Hello!").
		Line("This is a test notification.").
		Action("View", "https://example.com").
		Outro("Thanks for reading.")
}

func (n *testNotification) ToDatabase(notifiable interface{}) *DatabaseMessage {
	return NewDatabaseMessage("test.notification").
		Set("subject", n.subject)
}

func (n *testNotification) ToBroadcast(notifiable interface{}) *BroadcastMessage {
	return NewBroadcastMessage("test.notification").
		Set("subject", n.subject)
}

// testChannel is an in-memory channel for testing.
type testChannel struct {
	mu   sync.Mutex
	sent []sentRecord
	err  error
}

type sentRecord struct {
	notifiable   interface{}
	notification Notification
}

func (c *testChannel) Send(ctx context.Context, notifiable interface{}, notification Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.sent = append(c.sent, sentRecord{notifiable: notifiable, notification: notification})
	return nil
}

// --- Tests ---

func TestNewMailMessage(t *testing.T) {
	msg := NewMailMessage().
		From("sender@example.com", "Sender").
		To("user@example.com").
		CC("cc@example.com").
		BCC("bcc@example.com").
		ReplyTo("reply@example.com").
		Subject("Test Subject").
		Greeting("Hello!").
		Line("First line.").
		Line("Second line.").
		Action("Click Me", "https://example.com/action").
		Outro("Thanks!").
		Priority(mail.HighPriority).
		Header("X-Custom", "value")

	if msg.GetFrom().Email != "sender@example.com" {
		t.Errorf("expected from email sender@example.com, got %s", msg.GetFrom().Email)
	}
	if msg.GetFrom().Name != "Sender" {
		t.Errorf("expected from name Sender, got %s", msg.GetFrom().Name)
	}
	if len(msg.GetTo()) != 1 || msg.GetTo()[0] != "user@example.com" {
		t.Errorf("expected to [user@example.com], got %v", msg.GetTo())
	}
	if len(msg.GetCC()) != 1 || msg.GetCC()[0] != "cc@example.com" {
		t.Errorf("expected cc [cc@example.com], got %v", msg.GetCC())
	}
	if len(msg.GetBCC()) != 1 || msg.GetBCC()[0] != "bcc@example.com" {
		t.Errorf("expected bcc [bcc@example.com], got %v", msg.GetBCC())
	}
	if msg.GetReplyTo() != "reply@example.com" {
		t.Errorf("expected reply-to reply@example.com, got %s", msg.GetReplyTo())
	}
	if msg.GetSubject() != "Test Subject" {
		t.Errorf("expected subject Test Subject, got %s", msg.GetSubject())
	}
	if msg.GetGreeting() != "Hello!" {
		t.Errorf("expected greeting Hello!, got %s", msg.GetGreeting())
	}
	if len(msg.GetLines()) != 2 {
		t.Errorf("expected 2 lines, got %d", len(msg.GetLines()))
	}
	if msg.GetAction() == nil || msg.GetAction().Text != "Click Me" {
		t.Error("expected action with text Click Me")
	}
	if msg.GetAction().URL != "https://example.com/action" {
		t.Errorf("expected action URL https://example.com/action, got %s", msg.GetAction().URL)
	}
	if len(msg.GetOutro()) != 1 || msg.GetOutro()[0] != "Thanks!" {
		t.Errorf("expected outro [Thanks!], got %v", msg.GetOutro())
	}
	if msg.GetPriority() != mail.HighPriority {
		t.Errorf("expected HighPriority, got %v", msg.GetPriority())
	}
	if msg.GetHeaders()["X-Custom"] != "value" {
		t.Errorf("expected header X-Custom=value, got %s", msg.GetHeaders()["X-Custom"])
	}
}

func TestMailMessageCustomBody(t *testing.T) {
	msg := NewMailMessage().
		TextBody("Plain text").
		HTMLBody("<h1>HTML</h1>")

	if msg.GetTextBody() != "Plain text" {
		t.Errorf("expected text body, got %s", msg.GetTextBody())
	}
	if msg.GetHTMLBody() != "<h1>HTML</h1>" {
		t.Errorf("expected html body, got %s", msg.GetHTMLBody())
	}
}

func TestMailMessageAttachments(t *testing.T) {
	msg := NewMailMessage().
		AttachData([]byte("file contents"), "test.txt", "text/plain")

	atts := msg.GetAttachments()
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	if atts[0].Name != "test.txt" {
		t.Errorf("expected attachment name test.txt, got %s", atts[0].Name)
	}
	if string(atts[0].Data) != "file contents" {
		t.Errorf("expected attachment data, got %s", string(atts[0].Data))
	}
}

func TestNewDatabaseMessage(t *testing.T) {
	msg := NewDatabaseMessage("order.shipped").
		Set("order_id", 123).
		Set("tracking", "ABC123")

	if msg.Type != "order.shipped" {
		t.Errorf("expected type order.shipped, got %s", msg.Type)
	}
	if msg.Data["order_id"] != 123 {
		t.Errorf("expected order_id 123, got %v", msg.Data["order_id"])
	}
	if msg.Data["tracking"] != "ABC123" {
		t.Errorf("expected tracking ABC123, got %v", msg.Data["tracking"])
	}
}

func TestNewBroadcastMessage(t *testing.T) {
	msg := NewBroadcastMessage("notification.new").
		On("private-user.1", "private-user.2").
		Set("title", "New Message")

	if msg.Event != "notification.new" {
		t.Errorf("expected event notification.new, got %s", msg.Event)
	}
	if len(msg.Channels) != 2 {
		t.Errorf("expected 2 channels, got %d", len(msg.Channels))
	}
	if msg.Data["title"] != "New Message" {
		t.Errorf("expected title New Message, got %v", msg.Data["title"])
	}
}

func TestSlackMessage(t *testing.T) {
	msg := NewSlackMessage().
		To("#general").
		Content("Test notification").
		AsUser("VelocityBot").
		WithIcon(":bell:")

	if msg.Channel != "#general" {
		t.Errorf("expected channel #general, got %s", msg.Channel)
	}
	if msg.Text != "Test notification" {
		t.Errorf("expected text, got %s", msg.Text)
	}
	if msg.Username != "VelocityBot" {
		t.Errorf("expected username VelocityBot, got %s", msg.Username)
	}
	if msg.IconEmoji != ":bell:" {
		t.Errorf("expected icon :bell:, got %s", msg.IconEmoji)
	}
}

func TestSlackMessageAttachment(t *testing.T) {
	msg := NewSlackMessage().
		Content("Order update").
		Attachment(func(a *SlackAttachment) {
			a.Color = "#36a64f"
			a.Title = "Order Shipped"
			a.Text = "Your order has shipped"
			a.Field("Order ID", "12345", true)
			a.Field("Status", "Shipped", true)
		})

	if len(msg.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(msg.Attachments))
	}
	att := msg.Attachments[0]
	if att.Color != "#36a64f" {
		t.Errorf("expected color #36a64f, got %s", att.Color)
	}
	if len(att.Fields) != 2 {
		t.Errorf("expected 2 fields, got %d", len(att.Fields))
	}
	if att.Fields[0].Title != "Order ID" {
		t.Errorf("expected field title Order ID, got %s", att.Fields[0].Title)
	}
}

func TestManagerSend(t *testing.T) {
	mgr := NewManager()
	ch := &testChannel{}
	mgr.SetChannel("test", ch)

	notifiable := &testNotifiable{email: "user@example.com", id: "1"}
	notification := &testNotification{subject: "Test", channels: []string{"test"}}

	err := mgr.Send(context.Background(), notifiable, notification)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ch.sent) != 1 {
		t.Fatalf("expected 1 sent notification, got %d", len(ch.sent))
	}
}

func TestManagerSendMultipleChannels(t *testing.T) {
	mgr := NewManager()
	ch1 := &testChannel{}
	ch2 := &testChannel{}
	mgr.SetChannel("ch1", ch1)
	mgr.SetChannel("ch2", ch2)

	notifiable := &testNotifiable{email: "user@example.com", id: "1"}
	notification := &testNotification{subject: "Multi", channels: []string{"ch1", "ch2"}}

	err := mgr.Send(context.Background(), notifiable, notification)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(ch1.sent) != 1 {
		t.Errorf("expected 1 sent on ch1, got %d", len(ch1.sent))
	}
	if len(ch2.sent) != 1 {
		t.Errorf("expected 1 sent on ch2, got %d", len(ch2.sent))
	}
}

func TestManagerSendChannelError(t *testing.T) {
	mgr := NewManager()
	ch := &testChannel{err: errors.New("send failed")}
	mgr.SetChannel("test", ch)

	notifiable := &testNotifiable{email: "user@example.com", id: "1"}
	notification := &testNotification{subject: "Test", channels: []string{"test"}}

	err := mgr.Send(context.Background(), notifiable, notification)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestManagerSendUnregisteredChannel(t *testing.T) {
	mgr := NewManager()

	notifiable := &testNotifiable{email: "user@example.com", id: "1"}
	notification := &testNotification{subject: "Test", channels: []string{"nonexistent"}}

	err := mgr.Send(context.Background(), notifiable, notification)
	if err == nil {
		t.Fatal("expected error for unregistered channel, got nil")
	}
}

func TestManagerSendEmptyVia(t *testing.T) {
	mgr := NewManager()

	notifiable := &testNotifiable{email: "user@example.com", id: "1"}
	notification := &testNotification{subject: "Test", channels: []string{}}

	err := mgr.Send(context.Background(), notifiable, notification)
	if err != nil {
		t.Fatalf("expected no error for empty via, got %v", err)
	}
}

func TestManagerSendMany(t *testing.T) {
	mgr := NewManager()
	ch := &testChannel{}
	mgr.SetChannel("test", ch)

	notifiables := []interface{}{
		&testNotifiable{email: "a@example.com", id: "1"},
		&testNotifiable{email: "b@example.com", id: "2"},
		&testNotifiable{email: "c@example.com", id: "3"},
	}
	notification := &testNotification{subject: "Batch", channels: []string{"test"}}

	err := mgr.SendMany(context.Background(), notifiables, notification)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ch.mu.Lock()
	defer ch.mu.Unlock()
	if len(ch.sent) != 3 {
		t.Fatalf("expected 3 sent notifications, got %d", len(ch.sent))
	}
}

func TestManagerEventDispatching(t *testing.T) {
	mgr := NewManager()
	ch := &testChannel{}
	mgr.SetChannel("test", ch)

	var dispatched []interface{}
	mgr.SetEventDispatcher(func(event interface{}) error {
		dispatched = append(dispatched, event)
		return nil
	})

	notifiable := &testNotifiable{email: "user@example.com", id: "1"}
	notification := &testNotification{subject: "Event Test", channels: []string{"test"}}

	err := mgr.Send(context.Background(), notifiable, notification)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(dispatched) != 1 {
		t.Fatalf("expected 1 dispatched event, got %d", len(dispatched))
	}

	sent, ok := dispatched[0].(*NotificationSent)
	if !ok {
		t.Fatalf("expected *NotificationSent, got %T", dispatched[0])
	}
	if sent.Channel != "test" {
		t.Errorf("expected channel 'test', got %s", sent.Channel)
	}
}

func TestManagerEventDispatchingOnFailure(t *testing.T) {
	mgr := NewManager()
	ch := &testChannel{err: errors.New("delivery failed")}
	mgr.SetChannel("test", ch)

	var dispatched []interface{}
	mgr.SetEventDispatcher(func(event interface{}) error {
		dispatched = append(dispatched, event)
		return nil
	})

	notifiable := &testNotifiable{email: "user@example.com", id: "1"}
	notification := &testNotification{subject: "Fail Test", channels: []string{"test"}}

	_ = mgr.Send(context.Background(), notifiable, notification)

	if len(dispatched) != 1 {
		t.Fatalf("expected 1 dispatched event, got %d", len(dispatched))
	}

	failed, ok := dispatched[0].(*NotificationFailed)
	if !ok {
		t.Fatalf("expected *NotificationFailed, got %T", dispatched[0])
	}
	if failed.Channel != "test" {
		t.Errorf("expected channel 'test', got %s", failed.Channel)
	}
	if failed.Error != "delivery failed" {
		t.Errorf("expected error 'delivery failed', got %s", failed.Error)
	}
}

func TestChannelRegistry(t *testing.T) {
	RegisterChannel("test-channel", func() (Channel, error) {
		return &testChannel{}, nil
	})

	names := RegisteredChannels()
	found := false
	for _, name := range names {
		if name == "test-channel" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected test-channel in registered channels")
	}
}

func TestNotificationEventNames(t *testing.T) {
	sent := &NotificationSent{}
	if sent.Name() != "notification.sent" {
		t.Errorf("expected notification.sent, got %s", sent.Name())
	}

	failed := &NotificationFailed{}
	if failed.Name() != "notification.failed" {
		t.Errorf("expected notification.failed, got %s", failed.Name())
	}
}

func TestNotificationVia(t *testing.T) {
	n := &testNotification{channels: []string{"mail", "database"}}
	via := n.Via(nil)
	if len(via) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(via))
	}
	if via[0] != "mail" || via[1] != "database" {
		t.Errorf("expected [mail database], got %v", via)
	}
}
