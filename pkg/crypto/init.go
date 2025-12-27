package crypto

import (
	"os"
	"strings"
)

// init automatically initializes the encryption from environment variables
func init() {
	// Only auto-initialize if CRYPTO_KEY is set
	cryptoKey := os.Getenv("CRYPTO_KEY")
	if cryptoKey == "" {
		// Also check for APP_KEY (Laravel compatibility)
		cryptoKey = os.Getenv("APP_KEY")
		if cryptoKey == "" {
			return
		}
	}

	// Get cipher configuration
	cipher := os.Getenv("CRYPTO_CIPHER")
	if cipher == "" {
		cipher = "AES-256-CBC" // Default cipher
	}

	// Get previous keys for rotation
	var previousKeys []string
	if oldKeys := os.Getenv("CRYPTO_OLD_KEYS"); oldKeys != "" {
		previousKeys = strings.Split(oldKeys, ",")
	}

	// Initialize the global encryptor
	config := Config{
		Key:          cryptoKey,
		PreviousKeys: previousKeys,
		Cipher:       cipher,
	}

	if err := Init(config); err != nil {
		// Silently fail if auto-init fails
		// This allows the package to be used without auto-init
		return
	}
}
