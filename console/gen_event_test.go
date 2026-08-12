package console

import (
	"os"
	"strings"
	"testing"
)

func TestGenEvent_CreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenEvent("UserRegistered", GenEventOptions{}); err != nil {
		t.Fatalf("GenEvent() error = %v", err)
	}

	content, err := os.ReadFile("internal/events/user_registered.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package events") {
		t.Error("expected package events")
	}
	if !strings.Contains(s, "type UserRegistered struct") {
		t.Error("expected UserRegistered struct")
	}
	if !strings.Contains(s, `"user.registered"`) {
		t.Error("expected dot-separated event name 'user.registered'")
	}
	if !strings.Contains(s, "func (e UserRegistered) Name() string") {
		t.Error("expected Name() method")
	}
}

func TestGenEvent_AlreadyExists(t *testing.T) {
	t.Chdir(t.TempDir())

	os.MkdirAll("internal/events", 0755)
	os.WriteFile("internal/events/user_registered.go", []byte("existing"), 0644)

	err := GenEvent("UserRegistered", GenEventOptions{})
	if err == nil {
		t.Error("expected error when event already exists")
	}
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got: %v", err)
	}
}

func TestGenEvent_StripsSuffix(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenEvent("OrderPlacedEvent", GenEventOptions{}); err != nil {
		t.Fatalf("GenEvent() error = %v", err)
	}

	if _, err := os.Stat("internal/events/order_placed.go"); err != nil {
		t.Error("expected order_placed.go (Event suffix should be stripped)")
	}
}

func TestGenEvent_VerifiesContent(t *testing.T) {
	t.Chdir(t.TempDir())

	if err := GenEvent("OrderPlaced", GenEventOptions{}); err != nil {
		t.Fatalf("GenEvent() error = %v", err)
	}

	content, err := os.ReadFile("internal/events/order_placed.go")
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "// OrderPlaced event") {
		t.Error("expected comment with event name")
	}
	if !strings.Contains(s, `"order.placed"`) {
		t.Error("expected dot-separated event name 'order.placed'")
	}
}

func TestToDotSeparated(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"UserRegistered", "user.registered"},
		{"OrderPlaced", "order.placed"},
		{"Payment", "payment"},
		{"UserProfileUpdated", "user.profile.updated"},
	}

	for _, tt := range tests {
		got := toDotSeparated(tt.input)
		if got != tt.expected {
			t.Errorf("toDotSeparated(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
