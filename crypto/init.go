package crypto

import (
	"os"
	"strings"
)

// ConfigFromEnv builds a Config from environment variables.
// It reads CRYPTO_KEY (or APP_KEY), CRYPTO_CIPHER, and CRYPTO_OLD_KEYS.
// Returns the config and true if a key was found, or a zero Config and false otherwise.
func ConfigFromEnv() (Config, bool) {
	cryptoKey := os.Getenv("CRYPTO_KEY")
	if cryptoKey == "" {
		cryptoKey = os.Getenv("APP_KEY")
		if cryptoKey == "" {
			return Config{}, false
		}
	}

	cipher := os.Getenv("CRYPTO_CIPHER")
	if cipher == "" {
		cipher = "AES-256-GCM" // Default cipher
	}

	var previousKeys []string
	if oldKeys := os.Getenv("CRYPTO_OLD_KEYS"); oldKeys != "" {
		previousKeys = strings.Split(oldKeys, ",")
	}

	return Config{
		Key:          cryptoKey,
		PreviousKeys: previousKeys,
		Cipher:       cipher,
	}, true
}
