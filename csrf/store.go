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
//
// Reads are snapshots: Get on a store that keeps the token in the request's
// session (stores.SessionBagStore) reads the session as the request loaded
// it, so a request loaded before another request rotated or revoked the
// token may still emit or validate the old token. Writes (LoadOrStore,
// Set, Delete, AtomicConsumer.ConsumeIfMatch) go to the store's shared
// record where it has one.
//
// A store is called while CSRF holds a lock for the request (reading a
// token single-flights per request), so it must not call back into CSRF
// token reads for the request it is serving: that call waits on the lock
// its own caller holds. A session scheme rotating or revoking a token
// calls it with no lock held; a store asking the scheme about that
// request meanwhile gets auth.ErrOperationInProgress.
type Store interface {
	// Get returns the token held for session id, or
	// stores.ErrTokenNotFound when none is held.
	Get(ctx context.Context, id string) (string, error)

	// Set stores token for session id, replacing any token held.
	Set(ctx context.Context, id string, token string) error

	// LoadOrStore returns the usable token held for session id
	// (loaded=true), or stores candidate for id and returns it
	// (loaded=false) when none is held: missing, expired, or otherwise
	// unusable by the store's own rules. The check and the store are one
	// step against every other caller sharing the store's record of id, so
	// of concurrent first reads of one session (tabs opened at once after
	// the token was revoked or expired) exactly one candidate is stored and
	// every reader is handed it. GetToken mints through it on a miss.
	LoadOrStore(ctx context.Context, id, candidate string) (held string, loaded bool, err error)

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
// Config.SingleUse requires it: NewE refuses single use with a Store that
// does not implement it. A store that cannot compare and remove in one
// step (e.g. a thin SQL store without row-level locking) should not
// implement this interface, and cannot serve single-use tokens.
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
