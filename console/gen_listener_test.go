package console

import (
	"os"
	"strings"
	"testing"
)

func TestGenListener_CreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenListener("SendWelcomeEmail", GenListenerOptions{}); err != nil {
		t.Fatalf("GenListener() error = %v", err)
	}

	content, err := os.ReadFile("internal/listeners/send_welcome_email.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package listeners") {
		t.Error("expected package listeners")
	}
	if !strings.Contains(s, "func SendWelcomeEmail(event interface{}) error") {
		t.Error("expected listener function signature")
	}
}

func TestGenListener_AlreadyExists(t *testing.T) {
	t.Chdir(t.TempDir())

	os.MkdirAll("internal/listeners", 0755)
	os.WriteFile("internal/listeners/send_welcome_email.go", []byte("existing"), 0644)

	err := GenListener("SendWelcomeEmail", GenListenerOptions{})
	if err == nil {
		t.Error("expected error when listener already exists")
	}
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}

func TestGenListener_StripsSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenListener("SendWelcomeEmailListener", GenListenerOptions{}); err != nil {
		t.Fatalf("GenListener() error = %v", err)
	}

	if _, err := os.Stat("internal/listeners/send_welcome_email.go"); err != nil {
		t.Error("expected send_welcome_email.go (Listener suffix should be stripped)")
	}
}

func TestGenListener_VerifiesContent(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenListener("NotifyAdmin", GenListenerOptions{}); err != nil {
		t.Fatalf("GenListener() error = %v", err)
	}

	content, err := os.ReadFile("internal/listeners/notify_admin.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "// NotifyAdmin handles an event") {
		t.Error("expected comment with listener name")
	}
	if !strings.Contains(s, "func NotifyAdmin(") {
		t.Error("expected PascalCase function name")
	}
}
