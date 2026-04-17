package drivers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
