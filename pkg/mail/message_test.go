package mail

import (
	"context"
	"os"
	"testing"
)

func TestNewMessage(t *testing.T) {
	msg := NewMessage()

	if msg == nil {
		t.Error("Expected message to be created")
	}

	if msg.priority != NormalPriority {
		t.Error("Expected default priority to be NormalPriority")
	}

	if msg.to == nil || msg.cc == nil || msg.bcc == nil {
		t.Error("Expected slices to be initialized")
	}
}

func TestMessageFrom(t *testing.T) {
	msg := NewMessage().From("sender@example.com")

	if msg.GetFrom().Email != "sender@example.com" {
		t.Errorf("Expected sender@example.com, got %s", msg.GetFrom().Email)
	}

	msg = NewMessage().From("sender@example.com", "Sender Name")
	if msg.GetFrom().Name != "Sender Name" {
		t.Errorf("Expected 'Sender Name', got '%s'", msg.GetFrom().Name)
	}
}

func TestMessageTo(t *testing.T) {
	msg := NewMessage().
		To("user1@example.com").
		To("user2@example.com", "User Two")

	to := msg.GetTo()
	if len(to) != 2 {
		t.Errorf("Expected 2 recipients, got %d", len(to))
	}

	if to[0].Email != "user1@example.com" {
		t.Errorf("Expected user1@example.com, got %s", to[0].Email)
	}

	if to[1].Name != "User Two" {
		t.Errorf("Expected 'User Two', got '%s'", to[1].Name)
	}
}

func TestMessageCC(t *testing.T) {
	msg := NewMessage().
		CC("cc1@example.com").
		CC("cc2@example.com", "CC Two")

	cc := msg.GetCC()
	if len(cc) != 2 {
		t.Errorf("Expected 2 CC recipients, got %d", len(cc))
	}

	if cc[0].Email != "cc1@example.com" {
		t.Errorf("Expected cc1@example.com, got %s", cc[0].Email)
	}
}

func TestMessageBCC(t *testing.T) {
	msg := NewMessage().
		BCC("bcc1@example.com").
		BCC("bcc2@example.com", "BCC Two")

	bcc := msg.GetBCC()
	if len(bcc) != 2 {
		t.Errorf("Expected 2 BCC recipients, got %d", len(bcc))
	}

	if bcc[0].Email != "bcc1@example.com" {
		t.Errorf("Expected bcc1@example.com, got %s", bcc[0].Email)
	}
}

func TestMessageReplyTo(t *testing.T) {
	msg := NewMessage().
		ReplyTo("reply@example.com").
		ReplyTo("reply2@example.com", "Reply Two")

	replyTo := msg.GetReplyTo()
	if len(replyTo) != 2 {
		t.Errorf("Expected 2 reply-to addresses, got %d", len(replyTo))
	}

	if replyTo[0].Email != "reply@example.com" {
		t.Errorf("Expected reply@example.com, got %s", replyTo[0].Email)
	}
}

func TestMessageSubject(t *testing.T) {
	msg := NewMessage().Subject("Test Subject")

	if msg.GetSubject() != "Test Subject" {
		t.Errorf("Expected 'Test Subject', got '%s'", msg.GetSubject())
	}
}

func TestMessageBody(t *testing.T) {
	msg := NewMessage().Body("Plain text body")

	if msg.GetTextBody() != "Plain text body" {
		t.Errorf("Expected 'Plain text body', got '%s'", msg.GetTextBody())
	}
}

func TestMessageTextBody(t *testing.T) {
	msg := NewMessage().TextBody("Text body")

	if msg.GetTextBody() != "Text body" {
		t.Errorf("Expected 'Text body', got '%s'", msg.GetTextBody())
	}
}

func TestMessageHTMLBody(t *testing.T) {
	msg := NewMessage().HTMLBody("<h1>HTML body</h1>")

	if msg.GetHTMLBody() != "<h1>HTML body</h1>" {
		t.Errorf("Expected '<h1>HTML body</h1>', got '%s'", msg.GetHTMLBody())
	}
}

func TestMessageAttachData(t *testing.T) {
	data := []byte("test data")
	msg := NewMessage().AttachData(data, "test.txt", "text/plain")

	attachments := msg.GetAttachments()
	if len(attachments) != 1 {
		t.Errorf("Expected 1 attachment, got %d", len(attachments))
	}

	if attachments[0].Name != "test.txt" {
		t.Errorf("Expected 'test.txt', got '%s'", attachments[0].Name)
	}

	if attachments[0].ContentType != "text/plain" {
		t.Errorf("Expected 'text/plain', got '%s'", attachments[0].ContentType)
	}

	if string(attachments[0].Data) != "test data" {
		t.Errorf("Expected 'test data', got '%s'", string(attachments[0].Data))
	}
}

func TestMessageAttachFile(t *testing.T) {
	// Create a temp file
	tmpFile, err := os.CreateTemp("", "test-*.txt")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	content := "test file content"
	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}
	tmpFile.Close()

	msg := NewMessage().AttachFile(tmpFile.Name())

	attachments := msg.GetAttachments()
	if len(attachments) != 1 {
		t.Errorf("Expected 1 attachment, got %d", len(attachments))
	}

	if string(attachments[0].Data) != content {
		t.Errorf("Expected '%s', got '%s'", content, string(attachments[0].Data))
	}
}

func TestMessageAttachFilePanicsOnError(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic when attaching non-existent file")
		}
	}()

	NewMessage().AttachFile("/nonexistent/file.txt")
}

func TestMessageHeader(t *testing.T) {
	msg := NewMessage().
		Header("X-Custom-Header", "custom-value").
		Header("X-Another-Header", "another-value")

	headers := msg.GetHeaders()
	if len(headers) != 2 {
		t.Errorf("Expected 2 headers, got %d", len(headers))
	}

	if headers["X-Custom-Header"] != "custom-value" {
		t.Errorf("Expected 'custom-value', got '%s'", headers["X-Custom-Header"])
	}
}

func TestMessagePriority(t *testing.T) {
	msg := NewMessage().Priority(HighPriority)

	if msg.GetPriority() != HighPriority {
		t.Errorf("Expected HighPriority, got %d", msg.GetPriority())
	}
}

func TestMessageSend(t *testing.T) {
	mock := &mockMailer{sent: make([]*Message, 0)}
	SetDefaultMailer(mock)

	msg := NewMessage().
		To("test@example.com").
		Subject("Test").
		Body("Hello")

	err := msg.Send()
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if len(mock.sent) != 1 {
		t.Errorf("Expected 1 message sent, got %d", len(mock.sent))
	}
}

func TestMessageSendWithContext(t *testing.T) {
	mock := &mockMailer{sent: make([]*Message, 0)}
	SetDefaultMailer(mock)

	msg := NewMessage().
		To("test@example.com").
		Subject("Test").
		Body("Hello")

	ctx := context.Background()
	err := msg.SendWithContext(ctx)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if len(mock.sent) != 1 {
		t.Errorf("Expected 1 message sent, got %d", len(mock.sent))
	}
}

func TestMessageFluentAPI(t *testing.T) {
	msg := NewMessage().
		From("sender@example.com", "Sender").
		To("recipient@example.com", "Recipient").
		CC("cc@example.com").
		BCC("bcc@example.com").
		ReplyTo("reply@example.com").
		Subject("Fluent API Test").
		TextBody("Plain text").
		HTMLBody("<p>HTML text</p>").
		Header("X-Custom", "value").
		Priority(HighPriority).
		AttachData([]byte("data"), "file.txt", "text/plain")

	// Verify all fields were set
	if msg.GetFrom().Email != "sender@example.com" {
		t.Error("From not set")
	}
	if len(msg.GetTo()) != 1 {
		t.Error("To not set")
	}
	if len(msg.GetCC()) != 1 {
		t.Error("CC not set")
	}
	if len(msg.GetBCC()) != 1 {
		t.Error("BCC not set")
	}
	if len(msg.GetReplyTo()) != 1 {
		t.Error("ReplyTo not set")
	}
	if msg.GetSubject() != "Fluent API Test" {
		t.Error("Subject not set")
	}
	if msg.GetTextBody() != "Plain text" {
		t.Error("TextBody not set")
	}
	if msg.GetHTMLBody() != "<p>HTML text</p>" {
		t.Error("HTMLBody not set")
	}
	if len(msg.GetHeaders()) != 1 {
		t.Error("Headers not set")
	}
	if msg.GetPriority() != HighPriority {
		t.Error("Priority not set")
	}
	if len(msg.GetAttachments()) != 1 {
		t.Error("Attachments not set")
	}
}

func TestMessageTemplate(t *testing.T) {
	// Create temp template directory
	tmpDir, err := os.MkdirTemp("", "mail-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	templatesDir := tmpDir + "/emails"
	os.MkdirAll(templatesDir, 0755)

	templateContent := `<h1>Hello {{.Name}}!</h1>`
	templateFile := templatesDir + "/test.html"
	if err := os.WriteFile(templateFile, []byte(templateContent), 0644); err != nil {
		t.Fatalf("Failed to write template: %v", err)
	}

	// Set template path to our temp directory
	SetTemplatePath(templatesDir)
	defer SetTemplatePath("resources/views/emails") // Restore default

	data := struct{ Name string }{Name: "World"}
	msg := NewMessage().Template("test", data)

	expected := "<h1>Hello World!</h1>"
	if msg.GetHTMLBody() != expected {
		t.Errorf("Expected '%s', got '%s'", expected, msg.GetHTMLBody())
	}
}

func TestMessageTemplatePanicsOnError(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic when template file not found")
		}
	}()

	NewMessage().Template("nonexistent", nil)
}
