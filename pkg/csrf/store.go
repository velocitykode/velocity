package csrf

// Store defines the interface for CSRF token storage
type Store interface {
	// Get retrieves a token for the given session/identifier
	Get(id string) (string, error)

	// Set stores a token for the given session/identifier
	Set(id string, token string) error

	// Delete removes a token
	Delete(id string) error

	// Exists checks if a token exists
	Exists(id string) bool
}
