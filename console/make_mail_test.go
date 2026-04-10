package console

import (
	"os"
	"strings"
	"testing"
)

func TestMakeMail_CreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeMail("WelcomeEmail", MakeMailOptions{}); err != nil {
		t.Fatalf("MakeMail() error = %v", err)
	}

	content, err := os.ReadFile("internal/mail/welcome_email.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package mail") {
		t.Error("expected package mail")
	}
	if !strings.Contains(s, "type WelcomeEmail struct") {
		t.Error("expected WelcomeEmail struct")
	}
	if !strings.Contains(s, "func (m WelcomeEmail) Envelope()") {
		t.Error("expected Envelope method")
	}
	if !strings.Contains(s, "func (m WelcomeEmail) Content()") {
		t.Error("expected Content method")
	}
	if !strings.Contains(s, "func (m WelcomeEmail) Build()") {
		t.Error("expected Build method")
	}
}

func TestMakeMail_StripsSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeMail("OrderConfirmationMail", MakeMailOptions{}); err != nil {
		t.Fatalf("MakeMail() error = %v", err)
	}

	if _, err := os.Stat("internal/mail/order_confirmation.go"); err != nil {
		t.Error("expected order_confirmation.go (Mail suffix should be stripped)")
	}

	content, err := os.ReadFile("internal/mail/order_confirmation.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	if !strings.Contains(string(content), "type OrderConfirmation struct") {
		t.Error("expected OrderConfirmation struct (Mail suffix stripped)")
	}
}

func TestMakeMail_StripsMailableSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeMail("InvoiceMailable", MakeMailOptions{}); err != nil {
		t.Fatalf("MakeMail() error = %v", err)
	}

	if _, err := os.Stat("internal/mail/invoice.go"); err != nil {
		t.Error("expected invoice.go (Mailable suffix should be stripped)")
	}

	content, err := os.ReadFile("internal/mail/invoice.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	if !strings.Contains(string(content), "type Invoice struct") {
		t.Error("expected Invoice struct (Mailable suffix stripped)")
	}
}

func TestMakeMail_AlreadyExists(t *testing.T) {
	t.Chdir(t.TempDir())

	os.MkdirAll("internal/mail", 0755)
	os.WriteFile("internal/mail/welcome_email.go", []byte("existing"), 0644)

	err := MakeMail("WelcomeEmail", MakeMailOptions{})
	if err == nil {
		t.Error("expected error when mailable already exists")
	}
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}
