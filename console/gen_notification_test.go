package console

import (
	"os"
	"strings"
	"testing"
)

func TestGenNotification_CreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenNotification("InvoicePaid", GenNotificationOptions{}); err != nil {
		t.Fatalf("GenNotification() error = %v", err)
	}

	content, err := os.ReadFile("internal/notifications/invoice_paid.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package notifications") {
		t.Error("expected package notifications")
	}
	if !strings.Contains(s, "type InvoicePaid struct") {
		t.Error("expected InvoicePaid struct")
	}
	if !strings.Contains(s, "func (n InvoicePaid) Via(notifiable interface{}) []string") {
		t.Error("expected Via method")
	}
	if !strings.Contains(s, "func (n InvoicePaid) ToMail(notifiable interface{})") {
		t.Error("expected ToMail method")
	}
}

func TestGenNotification_StripsSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenNotification("OrderShippedNotification", GenNotificationOptions{}); err != nil {
		t.Fatalf("GenNotification() error = %v", err)
	}

	if _, err := os.Stat("internal/notifications/order_shipped.go"); err != nil {
		t.Error("expected order_shipped.go (Notification suffix should be stripped)")
	}

	content, err := os.ReadFile("internal/notifications/order_shipped.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	if !strings.Contains(string(content), "type OrderShipped struct") {
		t.Error("expected OrderShipped struct (Notification suffix stripped)")
	}
}

func TestGenNotification_AlreadyExists(t *testing.T) {
	t.Chdir(t.TempDir())

	os.MkdirAll("internal/notifications", 0755)
	os.WriteFile("internal/notifications/invoice_paid.go", []byte("existing"), 0644)

	err := GenNotification("InvoicePaid", GenNotificationOptions{})
	if err == nil {
		t.Error("expected error when notification already exists")
	}
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}
