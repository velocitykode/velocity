package console

import (
	"os"
	"strings"
	"testing"
)

func TestGenJob_CreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenJob("ProcessPayment", GenJobOptions{}); err != nil {
		t.Fatalf("GenJob() error = %v", err)
	}

	content, err := os.ReadFile("internal/jobs/process_payment.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package jobs") {
		t.Error("expected package jobs")
	}
	if !strings.Contains(s, "type ProcessPayment struct") {
		t.Error("expected ProcessPayment struct")
	}
	if !strings.Contains(s, "func (j ProcessPayment) Handle() error") {
		t.Error("expected Handle method")
	}
	if !strings.Contains(s, "func (j ProcessPayment) Failed(err error)") {
		t.Error("expected Failed method")
	}
	if !strings.Contains(s, "func (j ProcessPayment) MaxAttempts() int") {
		t.Error("expected MaxAttempts method")
	}
}

func TestGenJob_StripsSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenJob("SendEmailJob", GenJobOptions{}); err != nil {
		t.Fatalf("GenJob() error = %v", err)
	}

	if _, err := os.Stat("internal/jobs/send_email.go"); err != nil {
		t.Error("expected send_email.go (Job suffix should be stripped)")
	}

	content, err := os.ReadFile("internal/jobs/send_email.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	if !strings.Contains(string(content), "type SendEmail struct") {
		t.Error("expected SendEmail struct (Job suffix stripped)")
	}
}

func TestGenJob_AlreadyExists(t *testing.T) {
	t.Chdir(t.TempDir())

	os.MkdirAll("internal/jobs", 0755)
	os.WriteFile("internal/jobs/process_payment.go", []byte("existing"), 0644)

	err := GenJob("ProcessPayment", GenJobOptions{})
	if err == nil {
		t.Error("expected error when job already exists")
	}
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}
