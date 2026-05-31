package mailgun

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/mail"
)

func TestNewMailgunDriver(t *testing.T) {
	config := mail.MailgunConfig{
		Domain:   "mg.example.com",
		Secret:   "test-api-key",
		Endpoint: "https://api.mailgun.net/v3",
	}

	driver, err := NewMailgunDriver(config, "", "")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if driver == nil {
		t.Fatal("Expected driver to be created")
	}

	if driver.domain != "mg.example.com" {
		t.Errorf("Expected domain to be 'mg.example.com', got %s", driver.domain)
	}

	if driver.apiKey != "test-api-key" {
		t.Errorf("Expected apiKey to be 'test-api-key', got %s", driver.apiKey)
	}

	if driver.endpoint != "https://api.mailgun.net/v3" {
		t.Errorf("Expected endpoint to be set, got %s", driver.endpoint)
	}
}

func TestNewMailgunDriverNoDomain(t *testing.T) {
	config := mail.MailgunConfig{
		Secret: "test-api-key",
	}

	_, err := NewMailgunDriver(config, "", "")
	if err == nil {
		t.Error("Expected error when MAILGUN_DOMAIN not set")
	}
}

func TestNewMailgunDriverNoSecret(t *testing.T) {
	config := mail.MailgunConfig{
		Domain: "mg.example.com",
	}

	_, err := NewMailgunDriver(config, "", "")
	if err == nil {
		t.Error("Expected error when MAILGUN_SECRET not set")
	}
}

func TestNewMailgunDriverDefaultEndpoint(t *testing.T) {
	config := mail.MailgunConfig{
		Domain: "mg.example.com",
		Secret: "test-api-key",
	}

	driver, err := NewMailgunDriver(config, "", "")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if driver.endpoint != "https://api.mailgun.net/v3" {
		t.Errorf("Expected default endpoint, got %s", driver.endpoint)
	}
}

func TestMailgunDriverVerifyWebhookSignature(t *testing.T) {
	config := mail.MailgunConfig{
		Domain:            "mg.example.com",
		Secret:            "test-api-key",
		WebhookSigningKey: "test-signing-key",
	}

	driver, _ := NewMailgunDriver(config, "", "")

	// Compute valid HMAC-SHA256 signature
	mac := hmac.New(sha256.New, []byte("test-signing-key"))
	mac.Write([]byte("timestamp" + "token"))
	validSig := hex.EncodeToString(mac.Sum(nil))

	// Test with valid signature
	result := driver.VerifyWebhookSignature("timestamp", "token", validSig)
	if !result {
		t.Error("Expected signature verification to return true for valid signature")
	}

	// Test with invalid signature
	result = driver.VerifyWebhookSignature("timestamp", "token", "invalid-signature")
	if result {
		t.Error("Expected signature verification to return false for invalid signature")
	}

	// Test with empty signature
	result = driver.VerifyWebhookSignature("timestamp", "token", "")
	if result {
		t.Error("Expected signature verification to return false for empty signature")
	}
}

func TestMailgunDriverParseWebhook(t *testing.T) {
	config := mail.MailgunConfig{
		Domain: "mg.example.com",
		Secret: "test-api-key",
	}

	driver, _ := NewMailgunDriver(config, "", "")

	jsonData := []byte(`{"event": "delivered", "message": {"headers": {"message-id": "test"}}}`)

	event, err := driver.ParseWebhook(jsonData)
	if err != nil {
		t.Errorf("Expected no error parsing webhook, got %v", err)
	}

	if event == nil {
		t.Error("Expected event to be parsed")
	}

	if event["event"] != "delivered" {
		t.Error("Expected event type to be 'delivered'")
	}
}

func TestMailgunDriverParseWebhookInvalidJSON(t *testing.T) {
	config := mail.MailgunConfig{
		Domain: "mg.example.com",
		Secret: "test-api-key",
	}

	driver, _ := NewMailgunDriver(config, "", "")

	invalidJSON := []byte(`{invalid json}`)

	_, err := driver.ParseWebhook(invalidJSON)
	if err == nil {
		t.Error("Expected error parsing invalid JSON")
	}
}

// TestNewMailgunDriverRejectsHTTPEndpoint verifies that non-https endpoints
// are refused at construction.
func TestNewMailgunDriverRejectsHTTPEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		wantErr  bool
	}{
		{"https ok", "https://api.mailgun.net/v3", false},
		{"http rejected", "http://api.mailgun.net/v3", true},
		{"ftp rejected", "ftp://api.mailgun.net/v3", true},
		{"empty uses default", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewMailgunDriver(mail.MailgunConfig{
				Domain:   "mg.example.com",
				Secret:   "k",
				Endpoint: tc.endpoint,
			}, "", "")
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestMailgunDriverCRLFInjection verifies that CRLF characters in addr.Name
// are stripped before being included in the outgoing message form fields.
func TestMailgunDriverCRLFInjection(t *testing.T) {
	driver, err := NewMailgunDriver(mail.MailgunConfig{
		Domain: "mg.example.com",
		Secret: "key",
	}, "from@example.com", "Legit Sender")
	if err != nil {
		t.Fatalf("driver: %v", err)
	}

	cases := []struct {
		name string
		arg  string
	}{
		{"LF", "Foo\nBcc: evil@example.com"},
		{"CR", "Foo\rBcc: evil@example.com"},
		{"CRLF", "Foo\r\nBcc: evil@example.com"},
		{"multiline", "Foo\r\nBcc: a@b\r\nCc: c@d"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := mail.NewMessage().
				From("from@example.com", tc.arg).
				To("to@example.com", tc.arg).
				CC("cc@example.com", tc.arg).
				BCC("bcc@example.com", tc.arg).
				ReplyTo("reply@example.com", tc.arg).
				Subject("Subject").
				TextBody("body")

			var buf bytes.Buffer
			w := multipart.NewWriter(&buf)
			if err := driver.addFields(w, msg); err != nil {
				t.Fatalf("addFields: %v", err)
			}
			w.Close()

			out := buf.String()
			// The core defence: the attacker's CR/LF payload must be stripped.
			// After sanitisation, form-field values only contain normal
			// multipart framing CRLFs (between boundary lines and headers),
			// but no CR/LF embedded inside the attacker-controlled value.
			// We verify by splitting the body into fields and checking each
			// field's value for raw CR/LF.
			for _, field := range []string{
				`name="from"`, `name="to"`, `name="cc"`,
				`name="bcc"`, `name="h:Reply-To"`,
			} {
				idx := strings.Index(out, field)
				if idx < 0 {
					continue
				}
				// Value starts after the blank line following the header.
				rest := out[idx:]
				// The value is bounded by the next "\r\n--" (boundary separator).
				endIdx := strings.Index(rest, "\r\n--")
				if endIdx < 0 {
					continue
				}
				// Skip past `name="..."\r\n\r\n` into the value bytes.
				valStart := strings.Index(rest, "\r\n\r\n")
				if valStart < 0 || valStart >= endIdx {
					continue
				}
				value := rest[valStart+4 : endIdx]
				if strings.ContainsAny(value, "\r\n") {
					t.Errorf("field %s value contains CR/LF: %q", field, value)
				}
			}
		})
	}
}
