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

	encodedKey := base64.StdEncoding.EncodeToString(key)

	envPath := ".env"
	content, err := os.ReadFile(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.WriteFile(envPath, []byte(fmt.Sprintf("APP_KEY=%s\n", encodedKey)), 0600); err != nil {
				return fmt.Errorf("failed to create .env: %w", err)
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

	if err := os.WriteFile(envPath, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		return fmt.Errorf("failed to update .env: %w", err)
	}

	cli.Success("Application key set successfully")
	return nil
}
