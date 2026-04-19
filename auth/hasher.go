package auth

import (
	"errors"
	"fmt"
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
	cost          int
	requestedCost int  // original value passed to NewBcryptHasher / SetCost
	clampedAtInit bool // true when the constructor had to raise cost to the secure minimum
	logger        Logger
	mu            sync.RWMutex
}

// NewBcryptHasher creates a new bcrypt hasher.
// Minimum cost is 10 for security; lower values are overridden with a warning.
// The warning is emitted once a framework logger is installed via SetLogger.
// Callers constructing a hasher directly may install one via SetLogger.
func NewBcryptHasher(cost int) *BcryptHasher {
	effective, belowMin := clampBcryptCost(cost)
	return &BcryptHasher{
		cost:          effective,
		requestedCost: cost,
		clampedAtInit: belowMin,
	}
}

// clampBcryptCost coerces cost into the safe bcrypt range. Returns the
// effective cost and a boolean indicating whether the input was below the
// secure minimum (so callers can emit a warning when a logger is available).
func clampBcryptCost(cost int) (int, bool) {
	belowMin := cost > 0 && cost < minSecureBcryptCost
	if cost < minSecureBcryptCost {
		cost = minSecureBcryptCost
	}
	if cost > bcrypt.MaxCost {
		cost = bcrypt.MaxCost
	}
	return cost, belowMin
}

// SetLogger installs a logger used to warn about bcrypt cost clamping when
// SetCost is called with a value below the secure minimum. If the hasher
// was constructed with a sub-minimum cost, a one-shot warning is emitted
// now (and the pending flag cleared) so the event is surfaced through the
// framework logger rather than being lost before wiring completed. Nil
// disables logging.
func (h *BcryptHasher) SetLogger(l Logger) {
	h.mu.Lock()
	h.logger = l
	pending := h.clampedAtInit && l != nil
	requested := h.requestedCost
	effective := h.cost
	if pending {
		h.clampedAtInit = false
	}
	h.mu.Unlock()

	if pending {
		l.Warn("auth: bcrypt cost below secure minimum, clamped", "requested", requested, "minimum", minSecureBcryptCost, "using", effective)
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
	effective, belowMin := clampBcryptCost(cost)

	h.mu.Lock()
	h.cost = effective
	h.requestedCost = cost
	logger := h.logger
	h.mu.Unlock()

	if belowMin && logger != nil {
		logger.Warn("auth: bcrypt cost below secure minimum, clamped", "requested", cost, "minimum", minSecureBcryptCost, "using", effective)
	}
}
