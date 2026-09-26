package csrf

import (
	"context"

	"github.com/velocitykode/velocity/csrf/stores"
)

// Store keeps the CSRF token of each session. Every call carries the
// context of the request it serves (the request context, or the context a
// session scheme passes when it rotates or revokes a token), so a store can
// keep the token in the request's session: stores.SessionBagStore, which
// velocity.New installs, does. stores.MemoryStore keys a map by id instead
// and is the session-less default of NewE.
type Store interface {
	// Get returns the token held for session id, or
	// stores.ErrTokenNotFound when none is held.
	Get(ctx context.Context, id string) (string, error)

	// Set stores token for session id.
	Set(ctx context.Context, id string, token string) error

	// Delete removes the token held for session id. A missing token is
	// not an error.
	Delete(ctx context.Context, id string) error

	// Exists reports whether a token is held for session id.
	Exists(ctx context.Context, id string) bool
}

// AtomicConsumer is an optional capability a Store may implement for
// single-use tokens. ConsumeIfMatch reads the token held for id, compares it
// in constant time with expected, and removes it only when they match, as
// one step: of concurrent callers that share the store's consumption
// record, exactly one consumes a token. Which callers share that record is
// what ConsumptionScope reports (stores.ConsumedEverywhere or
// stores.ConsumedPerInstance), and it decides whether single use is exact
// across a deployment or only on each instance.
//
// Returned values:
//   - consumed=true  : a token was held for id, matched expected, and is
//     now consumed
//   - consumed=false : no token was held, it did not match, or it was
//     consumed before
//   - err != nil     : underlying store failure (network, etc.); consumed is
//     meaningless and must be ignored
//
// A store that cannot compare and remove in one step (e.g. a thin SQL store
// without row-level locking) should not implement this interface; the CSRF
// middleware then reads, compares and deletes under a per-process lock and
// logs a one-time warning that single use is exact per process only.
//
// Implementations MUST use constant-time comparison for the value match
// (crypto/subtle.ConstantTimeCompare) to avoid leaking a token-length
// timing oracle to an attacker who can pre-seed entries via the public
// refresh handler.
type AtomicConsumer interface {
	ConsumeIfMatch(ctx context.Context, id string, expected string) (consumed bool, err error)

	// ConsumptionScope reports which instances share the record
	// ConsumeIfMatch consults, and so how far its guarantee reaches.
	ConsumptionScope() stores.ConsumptionScope
}
