package drivers

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/mail"
)

func TestNewLocalDriver(t *testing.T) {
	// Test with SMTP configuration
	os.Setenv("MAIL_HOST", "localhost")
	os.Setenv("MAIL_PORT", "587")
	os.Setenv("MAIL_USERNAME", "user")
	os.Setenv("MAIL_PASSWORD", "pass")
	os.Setenv("MAIL_ENCRYPTION", "tls")
	os.Unsetenv("MAIL_SENDMAIL_PATH")

	driver, err := NewLocalDriver()
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if driver == nil {
		t.Error("Expected driver to be created")
	}
}

func TestNewLocalDriverWithSendmail(t *testing.T) {
	os.Setenv("MAIL_SENDMAIL_PATH", "/usr/sbin/sendmail")
	os.Unsetenv("MAIL_HOST")

	driver, err := NewLocalDriver()
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if driver == nil {
		t.Error("Expected driver to be created")
	}

	if driver.sendmail != "/usr/sbin/sendmail" {
		t.Errorf("Expected sendmail path to be set, got %s", driver.sendmail)
	}
}

func TestNewLocalDriverNoConfig(t *testing.T) {
	os.Unsetenv("MAIL_HOST")
	os.Unsetenv("MAIL_SENDMAIL_PATH")

	_, err := NewLocalDriver()
	if err == nil {
		t.Error("Expected error when no configuration is provided")
	}
}

func TestNewLocalDriverDefaultPort(t *testing.T) {
	os.Setenv("MAIL_HOST", "localhost")
	os.Unsetenv("MAIL_PORT")
	os.Unsetenv("MAIL_SENDMAIL_PATH")

	driver, err := NewLocalDriver()
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if driver.port != "587" {
		t.Errorf("Expected default port 587, got %s", driver.port)
	}
}

func TestLocalDriverBuildMessage(t *testing.T) {
	os.Setenv("MAIL_HOST", "localhost")
	os.Setenv("MAIL_FROM_ADDRESS", "from@example.com")
	os.Setenv("MAIL_FROM_NAME", "From Name")

	driver, _ := NewLocalDriver()

	msg := mail.NewMessage().
		To("to@example.com", "To Name").
		Subject("Test Subject").
		Body("Test body")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "From: From Name <from@example.com>") {
		t.Error("Expected From header in message")
	}

	if !strings.Contains(bodyStr, "To: To Name <to@example.com>") {
		t.Error("Expected To header in message")
	}

	if !strings.Contains(bodyStr, "Subject: Test Subject") {
		t.Error("Expected Subject header in message")
	}

	if !strings.Contains(bodyStr, "Test body") {
		t.Error("Expected body in message")
	}
}

func TestLocalDriverBuildMessageWithCC(t *testing.T) {
	os.Setenv("MAIL_HOST", "localhost")
	driver, _ := NewLocalDriver()

	msg := mail.NewMessage().
		To("to@example.com").
		CC("cc@example.com", "CC Name").
		Subject("Test")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Cc: CC Name <cc@example.com>") {
		t.Errorf("Expected CC header in message, got: %s", bodyStr)
	}
}

func TestLocalDriverBuildMessageWithReplyTo(t *testing.T) {
	os.Setenv("MAIL_HOST", "localhost")
	driver, _ := NewLocalDriver()

	msg := mail.NewMessage().
		To("to@example.com").
		ReplyTo("reply@example.com", "Reply Name").
		Subject("Test")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Reply-To: Reply Name <reply@example.com>") {
		t.Error("Expected Reply-To header in message")
	}
}

func TestLocalDriverBuildMessageWithPriority(t *testing.T) {
	os.Setenv("MAIL_HOST", "localhost")
	driver, _ := NewLocalDriver()

	t.Run("high priority", func(t *testing.T) {
		msg := mail.NewMessage().
			To("to@example.com").
			Subject("Test").
			Priority(mail.HighPriority)

		body := driver.buildMessage(msg)
		bodyStr := string(body)

		if !strings.Contains(bodyStr, "X-Priority: 1") {
			t.Error("Expected X-Priority: 1 for high priority")
		}
	})

	t.Run("low priority", func(t *testing.T) {
		msg := mail.NewMessage().
			To("to@example.com").
			Subject("Test").
			Priority(mail.LowPriority)

		body := driver.buildMessage(msg)
		bodyStr := string(body)

		if !strings.Contains(bodyStr, "X-Priority: 5") {
			t.Error("Expected X-Priority: 5 for low priority")
		}
	})
}

func TestLocalDriverBuildMessageWithCustomHeaders(t *testing.T) {
	os.Setenv("MAIL_HOST", "localhost")
	driver, _ := NewLocalDriver()

	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Test").
		Header("X-Custom-Header", "custom-value")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "X-Custom-Header: custom-value") {
		t.Error("Expected custom header in message")
	}
}

func TestLocalDriverBuildMessageWithHTMLBody(t *testing.T) {
	os.Setenv("MAIL_HOST", "localhost")
	driver, _ := NewLocalDriver()

	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Test").
		HTMLBody("<h1>HTML Content</h1>")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Content-Type: text/html") {
		t.Error("Expected HTML content type")
	}

	if !strings.Contains(bodyStr, "<h1>HTML Content</h1>") {
		t.Error("Expected HTML body in message")
	}
}

func TestLocalDriverBuildMessageWithBothBodies(t *testing.T) {
	os.Setenv("MAIL_HOST", "localhost")
	driver, _ := NewLocalDriver()

	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Test").
		TextBody("Plain text").
		HTMLBody("<h1>HTML</h1>")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "multipart/alternative") {
		t.Error("Expected multipart/alternative for both text and HTML")
	}

	if !strings.Contains(bodyStr, "Plain text") {
		t.Error("Expected plain text body")
	}

	if !strings.Contains(bodyStr, "<h1>HTML</h1>") {
		t.Error("Expected HTML body")
	}
}

func TestLocalDriverBuildMessageWithAttachments(t *testing.T) {
	os.Setenv("MAIL_HOST", "localhost")
	driver, _ := NewLocalDriver()

	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Test").
		Body("Body with attachment").
		AttachData([]byte("file content"), "test.txt", "text/plain")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "multipart/mixed") {
		t.Error("Expected multipart/mixed for attachments")
	}

	if !strings.Contains(bodyStr, "test.txt") {
		t.Error("Expected attachment filename")
	}

	if !strings.Contains(bodyStr, "Content-Transfer-Encoding: base64") {
		t.Error("Expected base64 encoding for attachment")
	}
}

func TestLocalDriverBuildMessageFromEnv(t *testing.T) {
	os.Setenv("MAIL_HOST", "localhost")
	os.Setenv("MAIL_FROM_ADDRESS", "default@example.com")
	os.Setenv("MAIL_FROM_NAME", "Default Sender")

	driver, _ := NewLocalDriver()

	// Message without explicit from
	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Test")

	body := driver.buildMessage(msg)
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "From: Default Sender <default@example.com>") {
		t.Error("Expected default from address from environment")
	}
}

func TestLocalDriverSendViaSMTPNoRecipients(t *testing.T) {
	os.Setenv("MAIL_HOST", "localhost")
	driver, _ := NewLocalDriver()

	msg := mail.NewMessage().Subject("Test")

	err := driver.sendViaSMTP(context.Background(), msg)
	if err == nil {
		t.Error("Expected error when no recipients specified")
	}

	if !strings.Contains(err.Error(), "no recipients") {
		t.Errorf("Expected 'no recipients' error, got %v", err)
	}
}

func TestLocalDriverSendViaSendmailNoRecipients(t *testing.T) {
	os.Setenv("MAIL_SENDMAIL_PATH", "/usr/sbin/sendmail")
	os.Unsetenv("MAIL_HOST")
	driver, _ := NewLocalDriver()

	msg := mail.NewMessage().Subject("Test")

	err := driver.sendViaSendmail(context.Background(), msg)
	if err == nil {
		t.Error("Expected error when no recipients specified")
	}

	if !strings.Contains(err.Error(), "no recipients") {
		t.Errorf("Expected 'no recipients' error, got %v", err)
	}
}
