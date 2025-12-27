package drivers

import (
	"os"
	"testing"
)

func TestNewMailgunDriver(t *testing.T) {
	os.Setenv("MAILGUN_DOMAIN", "mg.example.com")
	os.Setenv("MAILGUN_SECRET", "test-api-key")
	os.Setenv("MAILGUN_ENDPOINT", "https://api.mailgun.net/v3")

	driver, err := NewMailgunDriver()
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if driver == nil {
		t.Error("Expected driver to be created")
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
	os.Unsetenv("MAILGUN_DOMAIN")
	os.Setenv("MAILGUN_SECRET", "test-api-key")

	_, err := NewMailgunDriver()
	if err == nil {
		t.Error("Expected error when MAILGUN_DOMAIN not set")
	}
}

func TestNewMailgunDriverNoSecret(t *testing.T) {
	os.Setenv("MAILGUN_DOMAIN", "mg.example.com")
	os.Unsetenv("MAILGUN_SECRET")

	_, err := NewMailgunDriver()
	if err == nil {
		t.Error("Expected error when MAILGUN_SECRET not set")
	}
}

func TestNewMailgunDriverDefaultEndpoint(t *testing.T) {
	os.Setenv("MAILGUN_DOMAIN", "mg.example.com")
	os.Setenv("MAILGUN_SECRET", "test-api-key")
	os.Unsetenv("MAILGUN_ENDPOINT")

	driver, err := NewMailgunDriver()
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if driver.endpoint != "https://api.mailgun.net/v3" {
		t.Errorf("Expected default endpoint, got %s", driver.endpoint)
	}
}

func TestMailgunDriverVerifyWebhookSignature(t *testing.T) {
	os.Setenv("MAILGUN_DOMAIN", "mg.example.com")
	os.Setenv("MAILGUN_SECRET", "test-api-key")

	driver, _ := NewMailgunDriver()

	// Test with non-empty signature (placeholder test)
	result := driver.VerifyWebhookSignature("timestamp", "token", "signature")
	if !result {
		t.Error("Expected signature verification to return true for non-empty signature")
	}

	// Test with empty signature
	result = driver.VerifyWebhookSignature("timestamp", "token", "")
	if result {
		t.Error("Expected signature verification to return false for empty signature")
	}
}

func TestMailgunDriverParseWebhook(t *testing.T) {
	os.Setenv("MAILGUN_DOMAIN", "mg.example.com")
	os.Setenv("MAILGUN_SECRET", "test-api-key")

	driver, _ := NewMailgunDriver()

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
	os.Setenv("MAILGUN_DOMAIN", "mg.example.com")
	os.Setenv("MAILGUN_SECRET", "test-api-key")

	driver, _ := NewMailgunDriver()

	invalidJSON := []byte(`{invalid json}`)

	_, err := driver.ParseWebhook(invalidJSON)
	if err == nil {
		t.Error("Expected error parsing invalid JSON")
	}
}
