package console

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDown_CreatesMarkerFile(t *testing.T) {
	chdir(t, t.TempDir())

	if err := Down(DownOptions{}); err != nil {
		t.Fatalf("Down() error = %v", err)
	}

	path := filepath.Join(".vel", "down")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("expected .vel/down marker file to exist")
	}
}

func TestDown_WritesJSONPayload(t *testing.T) {
	chdir(t, t.TempDir())

	opts := DownOptions{
		Secret:     "bypass-secret",
		RetryAfter: 60,
	}
	if err := Down(opts); err != nil {
		t.Fatalf("Down() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(".vel", "down"))
	if err != nil {
		t.Fatalf("failed to read marker file: %v", err)
	}

	var payload downPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if payload.Secret != "bypass-secret" {
		t.Errorf("expected secret %q, got %q", "bypass-secret", payload.Secret)
	}
	if payload.RetryAfter != 60 {
		t.Errorf("expected retry_after %d, got %d", 60, payload.RetryAfter)
	}
	if payload.Time == "" {
		t.Error("expected time to be set")
	}
}

func TestDown_OmitsEmptyFields(t *testing.T) {
	chdir(t, t.TempDir())

	if err := Down(DownOptions{}); err != nil {
		t.Fatalf("Down() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(".vel", "down"))
	if err != nil {
		t.Fatalf("failed to read marker file: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if _, exists := raw["secret"]; exists {
		t.Error("expected secret to be omitted when empty")
	}
	if _, exists := raw["retry_after"]; exists {
		t.Error("expected retry_after to be omitted when zero")
	}
}

func TestUp_RemovesMarkerFile(t *testing.T) {
	chdir(t, t.TempDir())

	// Create the marker file first
	if err := Down(DownOptions{}); err != nil {
		t.Fatalf("Down() error = %v", err)
	}

	if err := Up(); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	path := filepath.Join(".vel", "down")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected .vel/down marker file to be removed")
	}
}

func TestUp_NoErrorWhenFileDoesNotExist(t *testing.T) {
	chdir(t, t.TempDir())

	err := Up()
	if err != nil {
		t.Fatalf("Up() returned error when file does not exist: %v", err)
	}
}
