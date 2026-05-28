package console

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	cli "github.com/velocitykode/velocity-cli"
)

// KeyGenerate generates a new APP_KEY and writes it to .env.
func KeyGenerate() error {
	// Generate 32-byte key
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("failed to generate key: %w", err)
	}

	// crypto.parseKey only base64-decodes values prefixed with "base64:".
	// Without the prefix the 44-char standard-base64 string is consumed as
	// 44 raw bytes and NewAESDriver rejects with ErrInvalidKeyLength
	// (cipher AES-256-GCM requires exactly 32 bytes). Emit the prefix so
	// the generated key survives parseKey -> base64 decode -> length check.
	encodedKey := "base64:" + base64.StdEncoding.EncodeToString(key)

	envPath := ".env"
	content, err := os.ReadFile(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.WriteFile(envPath, []byte(fmt.Sprintf("APP_KEY=%s\n", encodedKey)), secretFileMode); err != nil {
				return fmt.Errorf("failed to create .env: %w", err)
			}
			// os.WriteFile does NOT chmod a pre-existing file; the file may
			// have been raced into existence between Stat and WriteFile.
			// Force the tight mode so a loose pre-existing .env is tightened.
			if err := os.Chmod(envPath, secretFileMode); err != nil {
				return fmt.Errorf("failed to tighten .env permissions: %w", err)
			}
			cli.Success("Created .env with APP_KEY")
			return nil
		}
		return fmt.Errorf("failed to read .env: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, "APP_KEY=") {
			lines[i] = fmt.Sprintf("APP_KEY=%s", encodedKey)
			found = true
			break
		}
	}

	if !found {
		lines = append([]string{fmt.Sprintf("APP_KEY=%s", encodedKey)}, lines...)
	}

	if err := os.WriteFile(envPath, []byte(strings.Join(lines, "\n")), secretFileMode); err != nil {
		return fmt.Errorf("failed to update .env: %w", err)
	}
	// .env carries APP_KEY and other secrets; os.WriteFile preserves the
	// perms of a pre-existing file, so an older 0o644 .env would stay
	// world-readable across a key:generate run. Force the tight mode.
	if err := os.Chmod(envPath, secretFileMode); err != nil {
		return fmt.Errorf("failed to tighten .env permissions: %w", err)
	}

	cli.Success("Application key set successfully")
	return nil
}
