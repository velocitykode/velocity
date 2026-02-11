package auth

import (
	"errors"
	"fmt"
	"log"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

const minSecureBcryptCost = 10

// Hasher handles password hashing and verification
type Hasher interface {
	// Hash a password
	Hash(password string) (string, error)

	// Verify a password against a hash
	Verify(password string, hash string) bool

	// Check if hash needs rehashing
	NeedsRehash(hash string) bool
}

// BcryptHasher implements Hasher using bcrypt
type BcryptHasher struct {
	cost int
	mu   sync.RWMutex
}

// NewBcryptHasher creates a new bcrypt hasher.
// Minimum cost is 10 for security; lower values are overridden with a warning.
func NewBcryptHasher(cost int) *BcryptHasher {
	if cost < minSecureBcryptCost {
		if cost > 0 {
			log.Printf("auth: bcrypt cost %d is below minimum secure cost %d, using %d", cost, minSecureBcryptCost, minSecureBcryptCost)
		}
		cost = minSecureBcryptCost
	}
	if cost > bcrypt.MaxCost {
		cost = bcrypt.MaxCost
	}

	return &BcryptHasher{
		cost: cost,
	}
}

// Hash hashes a password using bcrypt
func (h *BcryptHasher) Hash(password string) (string, error) {
	if password == "" {
		return "", errors.New("password cannot be empty")
	}

	if len([]byte(password)) > 72 {
		return "", fmt.Errorf("password exceeds bcrypt's 72-byte limit (%d bytes)", len([]byte(password)))
	}

	h.mu.RLock()
	cost := h.cost
	h.mu.RUnlock()

	bytes, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		return "", err
	}

	return string(bytes), nil
}

// Verify verifies a password against a hash
func (h *BcryptHasher) Verify(password string, hash string) bool {
	if password == "" || hash == "" {
		return false
	}

	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// NeedsRehash checks if a hash needs rehashing
func (h *BcryptHasher) NeedsRehash(hash string) bool {
	if hash == "" {
		return true
	}

	h.mu.RLock()
	targetCost := h.cost
	h.mu.RUnlock()

	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		return true
	}

	return cost != targetCost
}

// SetCost updates the bcrypt cost factor
func (h *BcryptHasher) SetCost(cost int) {
	if cost < minSecureBcryptCost {
		if cost > 0 {
			log.Printf("auth: bcrypt cost %d is below minimum secure cost %d, using %d", cost, minSecureBcryptCost, minSecureBcryptCost)
		}
		cost = minSecureBcryptCost
	}
	if cost > bcrypt.MaxCost {
		cost = bcrypt.MaxCost
	}

	h.mu.Lock()
	h.cost = cost
	h.mu.Unlock()
}

// Global hasher instance
var (
	globalHasher Hasher
	hasherMux    sync.RWMutex
)

// InitHasher initializes the global hasher.
func InitHasher(hasher Hasher) {
	hasherMux.Lock()
	defer hasherMux.Unlock()
	globalHasher = hasher
}

// GetHasher returns the global hasher.
func GetHasher() Hasher {
	hasherMux.RLock()
	h := globalHasher
	hasherMux.RUnlock()

	if h != nil {
		return h
	}

	// Acquire write lock to initialize default hasher
	hasherMux.Lock()
	defer hasherMux.Unlock()

	// Double-check after acquiring write lock
	if globalHasher == nil {
		globalHasher = NewBcryptHasher(bcrypt.DefaultCost)
	}

	return globalHasher
}

// Hash hashes a password using the global hasher.
func Hash(password string) (string, error) {
	return GetHasher().Hash(password)
}

// Verify verifies a password against a hash using the global hasher.
func Verify(password string, hash string) bool {
	return GetHasher().Verify(password, hash)
}

// NeedsRehash checks if a hash needs rehashing using the global hasher.
func NeedsRehash(hash string) bool {
	return GetHasher().NeedsRehash(hash)
}
