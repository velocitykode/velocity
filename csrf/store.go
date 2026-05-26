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

// AtomicConsumer is an optional capability stores may implement to support
// safe cross-process single-use token enforcement. ConsumeIfMatch reads the
// token bound to id, compares it (constant-time) against expected, and
// deletes the entry only when they match - all in one atomic operation
// from the perspective of concurrent callers across all replicas.
//
// Returned values:
//   - consumed=true  : entry existed AND matched expected AND was deleted
//   - consumed=false : entry did not exist, expired, or did not match expected
//   - err != nil     : underlying store failure (network, etc.); consumed is
//     meaningless and must be ignored
//
// This is the only primitive that closes the multi-replica single-use race:
// the per-process sync.Mutex in csrf.CSRF cannot prevent replica A and
// replica B from both accepting the same token within the same instant.
// Stores that cannot implement an atomic compare-and-delete (e.g. a thin
// SQL store without row-level locking) should NOT implement this interface;
// the CSRF middleware will fall back to Get+Delete and emit a one-time
// warning so operators know their deployment is single-use-best-effort
// rather than single-use-exact.
//
// Implementations MUST use constant-time comparison for the value match
// (crypto/subtle.ConstantTimeCompare) to avoid leaking a token-length
// timing oracle to an attacker who can pre-seed entries via the public
// refresh handler.
type AtomicConsumer interface {
	ConsumeIfMatch(id string, expected string) (consumed bool, err error)
}
