package drivers

import (
	"context"
	"os"
	"testing"

	"github.com/velocitykode/velocity/mail"
)

func TestNewPostmarkDriver(t *testing.T) {
	os.Setenv("POSTMARK_TOKEN", "test-token")
	os.Setenv("POSTMARK_MESSAGE_STREAM", "outbound")

	driver, err := NewPostmarkDriver()
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if driver == nil {
		t.Fatal("Expected driver to be created")
	}

	if driver.token != "test-token" {
		t.Errorf("Expected token to be 'test-token', got %s", driver.token)
	}

	if driver.messageStream != "outbound" {
		t.Errorf("Expected message stream to be 'outbound', got %s", driver.messageStream)
	}
}

func TestNewPostmarkDriverNoToken(t *testing.T) {
	os.Unsetenv("POSTMARK_TOKEN")

	_, err := NewPostmarkDriver()
	if err == nil {
		t.Error("Expected error when POSTMARK_TOKEN not set")
	}
}

func TestNewPostmarkDriverDefaultStream(t *testing.T) {
	os.Setenv("POSTMARK_TOKEN", "test-token")
	os.Unsetenv("POSTMARK_MESSAGE_STREAM")

	driver, err := NewPostmarkDriver()
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if driver.messageStream != "outbound" {
		t.Errorf("Expected default message stream 'outbound', got %s", driver.messageStream)
	}
}

func TestPostmarkDriverBuildPayload(t *testing.T) {
	os.Setenv("POSTMARK_TOKEN", "test-token")
	os.Setenv("MAIL_FROM_ADDRESS", "from@example.com")
	os.Setenv("MAIL_FROM_NAME", "From Name")

	driver, _ := NewPostmarkDriver()

	msg := mail.NewMessage().
		To("to@example.com", "To Name").
		Subject("Test Subject").
		Body("Test body").
		HTMLBody("<h1>HTML</h1>")

	payload := driver.buildPayload(msg)

	if payload["From"] != "From Name <from@example.com>" {
		t.Errorf("Expected From to be formatted, got %v", payload["From"])
	}

	if payload["Subject"] != "Test Subject" {
		t.Error("Expected Subject to be set")
	}

	if payload["TextBody"] != "Test body" {
		t.Error("Expected TextBody to be set")
	}

	if payload["HtmlBody"] != "<h1>HTML</h1>" {
		t.Error("Expected HtmlBody to be set")
	}

	if payload["MessageStream"] != "outbound" {
		t.Error("Expected MessageStream to be set")
	}
}

func TestPostmarkDriverBuildPayloadMultipleRecipients(t *testing.T) {
	os.Setenv("POSTMARK_TOKEN", "test-token")
	driver, _ := NewPostmarkDriver()

	msg := mail.NewMessage().
		To("to1@example.com").
		To("to2@example.com").
		CC("cc1@example.com").
		CC("cc2@example.com").
		BCC("bcc1@example.com").
		Subject("Test")

	payload := driver.buildPayload(msg)

	// Multiple recipients should be in array
	if to, ok := payload["To"].([]string); !ok || len(to) != 2 {
		t.Errorf("Expected To to be array of 2, got %v", payload["To"])
	}

	if cc, ok := payload["Cc"].([]string); !ok || len(cc) != 2 {
		t.Errorf("Expected Cc to be array of 2, got %v", payload["Cc"])
	}

	// Single BCC recipient returns string, not array
	if bcc, ok := payload["Bcc"].(string); !ok || bcc == "" {
		t.Errorf("Expected Bcc to be string, got %v", payload["Bcc"])
	}
}

func TestPostmarkDriverBuildPayloadReplyTo(t *testing.T) {
	os.Setenv("POSTMARK_TOKEN", "test-token")
	driver, _ := NewPostmarkDriver()

	msg := mail.NewMessage().
		To("to@example.com").
		ReplyTo("reply@example.com", "Reply Name").
		Subject("Test")

	payload := driver.buildPayload(msg)

	if payload["ReplyTo"] != "Reply Name <reply@example.com>" {
		t.Errorf("Expected ReplyTo to be formatted, got %v", payload["ReplyTo"])
	}
}

func TestPostmarkDriverBuildPayloadHeaders(t *testing.T) {
	os.Setenv("POSTMARK_TOKEN", "test-token")
	driver, _ := NewPostmarkDriver()

	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Test").
		Header("X-Custom-1", "value1").
		Header("X-Custom-2", "value2")

	payload := driver.buildPayload(msg)

	headers, ok := payload["Headers"].([]map[string]string)
	if !ok {
		t.Error("Expected Headers to be array of maps")
	}

	if len(headers) != 2 {
		t.Errorf("Expected 2 headers, got %d", len(headers))
	}
}

func TestPostmarkDriverBuildPayloadAttachments(t *testing.T) {
	os.Setenv("POSTMARK_TOKEN", "test-token")
	driver, _ := NewPostmarkDriver()

	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Test").
		AttachData([]byte("content1"), "file1.txt", "text/plain").
		AttachData([]byte("content2"), "file2.pdf", "application/pdf")

	payload := driver.buildPayload(msg)

	attachments, ok := payload["Attachments"].([]map[string]interface{})
	if !ok {
		t.Error("Expected Attachments to be array of maps")
	}

	if len(attachments) != 2 {
		t.Errorf("Expected 2 attachments, got %d", len(attachments))
	}

	if attachments[0]["Name"] != "file1.txt" {
		t.Error("Expected first attachment name to be file1.txt")
	}

	if attachments[0]["ContentType"] != "text/plain" {
		t.Error("Expected first attachment content type to be text/plain")
	}
}

func TestPostmarkDriverBuildPayloadFromEnv(t *testing.T) {
	os.Setenv("POSTMARK_TOKEN", "test-token")
	os.Setenv("MAIL_FROM_ADDRESS", "default@example.com")
	os.Setenv("MAIL_FROM_NAME", "Default")

	driver, _ := NewPostmarkDriver()

	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Test")

	payload := driver.buildPayload(msg)

	if payload["From"] != "Default <default@example.com>" {
		t.Errorf("Expected From from env, got %v", payload["From"])
	}
}

func TestPostmarkDriverSendInvalidRequest(t *testing.T) {
	os.Setenv("POSTMARK_TOKEN", "test-token")
	driver, _ := NewPostmarkDriver()

	// Empty message
	msg := mail.NewMessage()

	// This should fail because the API won't accept empty message
	// But we can't test actual API calls without mocking HTTP
	ctx := context.Background()
	_ = driver.Send(ctx, msg)
	// We just verify it doesn't panic
}
