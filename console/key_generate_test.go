package console

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

func TestKeyGenerate_CreatesEnvFile(t *testing.T) {
	tmpDir := t.TempDir()
	os.Chdir(tmpDir)

	if err := KeyGenerate(); err != nil {
		t.Fatalf("KeyGenerate() error = %v", err)
	}

	content, err := os.ReadFile(".env")
	if err != nil {
		t.Fatalf("Failed to read .env: %v", err)
	}

	if !strings.HasPrefix(string(content), "APP_KEY=") {
		t.Errorf("expected APP_KEY= prefix, got: %s", content)
	}

	key := strings.TrimPrefix(strings.TrimSpace(string(content)), "APP_KEY=")
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		t.Fatalf("Key is not valid base64: %v", err)
	}
	if len(decoded) != 32 {
		t.Errorf("key length = %d, want 32", len(decoded))
	}
}

func TestKeyGenerate_UpdatesExistingKey(t *testing.T) {
	tmpDir := t.TempDir()
	os.Chdir(tmpDir)

	os.WriteFile(".env", []byte("DB_HOST=localhost\nAPP_KEY=old_key\nDB_PORT=5432\n"), 0644)

	if err := KeyGenerate(); err != nil {
		t.Fatalf("KeyGenerate() error = %v", err)
	}

	content, _ := os.ReadFile(".env")

	if strings.Contains(string(content), "old_key") {
		t.Error("old key should have been replaced")
	}
	if !strings.Contains(string(content), "DB_HOST=localhost") {
		t.Error("DB_HOST should be preserved")
	}
	if !strings.Contains(string(content), "DB_PORT=5432") {
		t.Error("DB_PORT should be preserved")
	}
}

func TestKeyGenerate_AddsKeyWhenMissing(t *testing.T) {
	tmpDir := t.TempDir()
	os.Chdir(tmpDir)

	os.WriteFile(".env", []byte("DB_HOST=localhost\n"), 0644)

	if err := KeyGenerate(); err != nil {
		t.Fatalf("KeyGenerate() error = %v", err)
	}

	content, _ := os.ReadFile(".env")

	if !strings.Contains(string(content), "APP_KEY=") {
		t.Error("APP_KEY should be added")
	}
	if !strings.Contains(string(content), "DB_HOST=localhost") {
		t.Error("existing content should be preserved")
	}
}
