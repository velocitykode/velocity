package bond

import (
	"encoding/json"

	"github.com/velocitykode/velocity/pkg/crypto"
)

// EncryptHistoryState encrypts page data for secure browser history storage
// Returns encrypted string or empty string if encryption is disabled
func (b *Bond) EncryptHistoryState(page Page) (string, error) {
	if !b.encryptHistory {
		return "", nil
	}

	// Serialize page to JSON
	data, err := json.Marshal(page)
	if err != nil {
		return "", err
	}

	// Encrypt using pkg/crypto
	return crypto.Encrypt(string(data))
}

// DecryptHistoryState decrypts page data from encrypted browser history
func (b *Bond) DecryptHistoryState(encrypted string) (*Page, error) {
	// Decrypt using pkg/crypto
	decrypted, err := crypto.Decrypt(encrypted)
	if err != nil {
		return nil, err
	}

	var page Page
	if err := json.Unmarshal([]byte(decrypted), &page); err != nil {
		return nil, err
	}

	return &page, nil
}
