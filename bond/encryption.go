package bond

import (
	"encoding/json"
	"fmt"
)

// EncryptHistoryState encrypts page data for secure browser history storage
// Returns encrypted string or empty string if encryption is disabled
func (b *Bond) EncryptHistoryState(page Page) (string, error) {
	if !b.encryptHistory {
		return "", nil
	}

	b.mu.RLock()
	enc := b.encryptor
	b.mu.RUnlock()
	if enc == nil {
		return "", fmt.Errorf("bond: encryptor not configured for history encryption")
	}

	// Serialize page to JSON
	data, err := json.Marshal(page)
	if err != nil {
		return "", err
	}

	return enc.Encrypt(string(data))
}

// DecryptHistoryState decrypts page data from encrypted browser history
func (b *Bond) DecryptHistoryState(encrypted string) (*Page, error) {
	b.mu.RLock()
	enc := b.encryptor
	b.mu.RUnlock()
	if enc == nil {
		return nil, fmt.Errorf("bond: encryptor not configured for history decryption")
	}

	decrypted, err := enc.Decrypt(encrypted)
	if err != nil {
		return nil, err
	}

	var page Page
	if err := json.Unmarshal([]byte(decrypted), &page); err != nil {
		return nil, err
	}

	return &page, nil
}
