package csrf

import (
	"crypto/rand"
	"encoding/base64"

	"github.com/velocitykode/velocity/crypto"
)

const tokenLength = 32

// GenerateToken creates a cryptographically secure random token
func GenerateToken() (string, error) {
	b := make([]byte, tokenLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// ValidateToken compares tokens using constant-time comparison to prevent timing attacks
func ValidateToken(token1, token2 string) bool {
	// Constant-time comparison prevents timing attacks
	return crypto.EqualString(token1, token2)
}
