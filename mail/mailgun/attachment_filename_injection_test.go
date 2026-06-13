package mailgun

import (
	"bytes"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/mail"
)

// TestAddAttachmentsSanitizesFilename asserts that CR/LF in an attachment name
// cannot inject a header break into the multipart Content-Disposition line.
// multipart.CreateFormFile's escapeQuotes handles quotes and backslashes but
// not CR/LF, so the name must be sanitized before it reaches the writer (B44).
func TestAddAttachmentsSanitizesFilename(t *testing.T) {
	driver, _ := NewMailgunDriver(mail.MailgunConfig{
		Domain: "mg.example.com",
		Secret: "k",
	}, "from@example.com", "From")

	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Test").
		Body("body").
		AttachData([]byte("data"), "a\r\nX-Evil: 1.txt", "text/plain")

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := driver.addAttachments(writer, msg); err != nil {
		t.Fatalf("addAttachments failed: %v", err)
	}
	writer.Close()

	body := buf.String()
	if strings.Contains(body, "\r\nX-Evil:") {
		t.Errorf("raw CR/LF reached Content-Disposition, header injection possible: %q", body)
	}
	if !strings.Contains(body, `filename="aX-Evil: 1.txt"`) {
		t.Errorf("expected sanitized filename in Content-Disposition, got: %q", body)
	}
}
