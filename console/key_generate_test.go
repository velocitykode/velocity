package console

import (
	"encoding/base64"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestKeyGenerate_CreatesEnvFile(t *testing.T) {
	t.Chdir(t.TempDir())

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
	t.Chdir(t.TempDir())

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
	t.Chdir(t.TempDir())

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

// TestKeyGenerate_CreatedEnvHasTightPerms verifies that the fresh-create
// path lands the .env file at 0o600. The file holds APP_KEY (used to
// derive cookie encryption + signed-URL + CSRF secrets) and must never
// default to world-readable.
func TestKeyGenerate_CreatedEnvHasTightPerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits not enforced on Windows")
	}
	t.Chdir(t.TempDir())

	if err := KeyGenerate(); err != nil {
		t.Fatalf("KeyGenerate() error = %v", err)
	}

	info, err := os.Stat(".env")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf(".env mode = %#o, want 0600", got)
	}
}

// TestKeyGenerate_TightensPreExistingLooseEnv verifies that a .env laid
// down by an editor or older binary at 0o644 is tightened to 0o600 by
// the next KeyGenerate run. os.WriteFile preserves pre-existing perms,
// so the explicit post-write Chmod is the only guarantee.
func TestKeyGenerate_TightensPreExistingLooseEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits not enforced on Windows")
	}
	t.Chdir(t.TempDir())

	if err := os.WriteFile(".env", []byte("DB_HOST=localhost\nAPP_KEY=old\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(".env", 0o644); err != nil {
		t.Fatalf("seed chmod: %v", err)
	}

	if err := KeyGenerate(); err != nil {
		t.Fatalf("KeyGenerate() error = %v", err)
	}

	info, err := os.Stat(".env")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf(".env mode = %#o, want 0600 (pre-existing loose mode must be tightened)", got)
	}
}
