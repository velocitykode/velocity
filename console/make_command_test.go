package console

import (
	"os"
	"strings"
	"testing"
)

func TestMakeCommand_CreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeCommand("SendEmails", MakeCommandOptions{}); err != nil {
		t.Fatalf("MakeCommand() error = %v", err)
	}

	content, err := os.ReadFile("internal/commands/send_emails.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package commands") {
		t.Error("expected package commands")
	}
	if !strings.Contains(s, "SendEmailsCommand") {
		t.Error("expected SendEmailsCommand struct")
	}
	if !strings.Contains(s, `"send-emails"`) {
		t.Error("expected kebab-case command name 'send-emails'")
	}
	if !strings.Contains(s, "Name() string") {
		t.Error("expected Name method")
	}
	if !strings.Contains(s, "Description() string") {
		t.Error("expected Description method")
	}
	if !strings.Contains(s, "Handle(s *velocity.Services, args []string) error") {
		t.Error("expected Handle method with Services and args parameters")
	}
	if !strings.Contains(s, `"github.com/velocitykode/velocity"`) {
		t.Error("expected velocity import")
	}
	if !strings.Contains(s, "Register this command in internal/commands/kernel.go") {
		t.Error("expected registration hint comment")
	}
}

func TestMakeCommand_StripsSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeCommand("SendEmailsCommand", MakeCommandOptions{}); err != nil {
		t.Fatalf("MakeCommand() error = %v", err)
	}

	if _, err := os.Stat("internal/commands/send_emails.go"); err != nil {
		t.Error("expected send_emails.go (Command suffix should be stripped)")
	}
}

func TestMakeCommand_AlreadyExists(t *testing.T) {
	t.Chdir(t.TempDir())

	os.MkdirAll("internal/commands", 0755)
	os.WriteFile("internal/commands/send_emails.go", []byte("existing"), 0644)

	err := MakeCommand("SendEmails", MakeCommandOptions{})
	if err == nil {
		t.Error("expected error when command already exists")
	}
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}

func TestMakeCommand_VerifiesContent(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := MakeCommand("PruneExpired", MakeCommandOptions{}); err != nil {
		t.Fatalf("MakeCommand() error = %v", err)
	}

	content, err := os.ReadFile("internal/commands/prune_expired.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "PruneExpiredCommand") {
		t.Error("expected PruneExpiredCommand struct")
	}
	if !strings.Contains(s, `"prune-expired"`) {
		t.Error("expected kebab-case command name 'prune-expired'")
	}
}

func TestToKebabCase(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"SendEmails", "send-emails"},
		{"PruneExpired", "prune-expired"},
		{"Backup", "backup"},
		{"GenerateReport", "generate-report"},
	}

	for _, tt := range tests {
		got := toKebabCase(tt.input)
		if got != tt.expected {
			t.Errorf("toKebabCase(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
