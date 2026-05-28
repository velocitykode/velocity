package auth

import (
	"context"
	"errors"
	"time"
)

// Server-side session store sentinel errors. These are framework-internal,
// callers should not surface them verbatim to clients (see SECURITY rule #6).
var (
	// ErrSessionNotFound is returned by ServerSessionStore implementations
	// when no session record exists for the supplied id.
	ErrSessionNotFound = errors.New("velocity/auth: session not found")

	// ErrSessionExpired is returned by ServerSessionStore.Get when a
	// stored session record has passed its ExpiresAt deadline.
	ErrSessionExpired = errors.New("velocity/auth: session expired")

	// ErrSessionRevoked is returned by SessionGuard.CheckWithError when
	// the request carries a valid cookie but the corresponding server-side
	// session record has been deleted (e.g. via Manager.RevokeSession or
	// RevokeAllSessions). Distinct from ErrSessionExpired: expired means
	// the cookie TTL passed; revoked means an administrative action removed
	// the session while the cookie was still in flight.
	ErrSessionRevoked = errors.New("velocity/auth: session revoked")

	// ErrNoServerSessionStore is returned by Manager.RevokeSession,
	// RevokeAllSessions, and ListActiveSessions when no server-side
	// session store has been installed via SetServerSessionStore.
	ErrNoServerSessionStore = errors.New("velocity/auth: no server session store configured")
)

// StoredSession is the server-side persisted view of a session. It is the
// shape passed across the ServerSessionStore boundary; the cookie / guard
// layer is unchanged. Data is the per-session bag (kept narrow on purpose,
// large blobs belong elsewhere).
type StoredSession struct {
	// ID is the opaque session identifier (matches the cookie value).
	ID string
	// UserID is the authenticated user identifier (string form for
	// driver portability).
	UserID string
	// Data carries arbitrary per-session values; drivers must accept any
	// JSON-shaped tree.
	Data map[string]any
	// CreatedAt records when the session was first written.
	CreatedAt time.Time
	// LastSeenAt records the most recent access (drivers should refresh
	// this on Put).
	LastSeenAt time.Time
	// ExpiresAt is the absolute expiry; sessions past this are treated
	// as ErrSessionExpired by Get and reaped by background sweeps.
	ExpiresAt time.Time
	// IPAddress is the remote address recorded at session creation, used
	// for the "your devices" listing UX. Optional.
	IPAddress string
	// UserAgent is the User-Agent header recorded at session creation,
	// used for the listing UX. Optional.
	UserAgent string
}

// SessionMeta is the listing-only projection of a StoredSession. It
// deliberately omits Data so administrative listings cannot leak
// per-session payloads.
type SessionMeta struct {
	// ID is the opaque session identifier.
	ID string
	// UserID is the authenticated user identifier.
	UserID string
	// CreatedAt records when the session was first written.
	CreatedAt time.Time
	// LastSeenAt records the most recent access.
	LastSeenAt time.Time
	// ExpiresAt is the absolute expiry timestamp.
	ExpiresAt time.Time
	// IPAddress is the remote address recorded at creation. Optional.
	IPAddress string
	// UserAgent is the User-Agent recorded at creation. Optional.
	UserAgent string
}

// ServerSessionStore is the driver-agnostic interface for persisting
// session records on the server. It exists alongside (not in place of)
// the cookie-side SessionStore in session.go: the cookie store handles
// per-request serialization, while ServerSessionStore underwrites
// administrative operations like "log out every device" and "list my
// active sessions". Implementations must be safe for concurrent use.
//
// Implementations must pass authtest.RunServerSessionStoreContractTests.
// See authtest for the executable specification.
type ServerSessionStore interface {
	// Get returns the StoredSession for id. Returns ErrSessionNotFound
	// when no record exists; returns ErrSessionExpired (and removes the
	// record) when the record has passed ExpiresAt.
	Get(ctx context.Context, id string) (*StoredSession, error)

	// Put creates or replaces a session record. Implementations must
	// update LastSeenAt to time.Now() and reject records with empty ID
	// or UserID.
	Put(ctx context.Context, session *StoredSession) error

	// Delete removes a single session by id. Returns nil when the
	// record does not exist (idempotent).
	Delete(ctx context.Context, id string) error

	// DeleteAllForUser removes every session record belonging to
	// userID. Returns nil when the user has no recorded sessions.
	DeleteAllForUser(ctx context.Context, userID string) error

	// ListForUser returns SessionMeta for every non-expired session
	// belonging to userID. The Data field is intentionally omitted.
	ListForUser(ctx context.Context, userID string) ([]*SessionMeta, error)
}

// ServerSessionStoreReceiver is the optional interface a Guard implements
// to opt into server-side session revocation. Manager.SetServerSessionStore
// walks all registered guards and propagates the store to every guard that
// implements this interface; guards that do not implement it (e.g. JWT) are
// skipped without error.
type ServerSessionStoreReceiver interface {
	SetServerSessionStore(store ServerSessionStore)
}

// RememberTokenClearer is the optional interface a Guard implements to
// invalidate persistent "remember me" credentials for a user.
// Manager.RevokeAllSessions walks every registered guard and invokes
// ClearRememberTokensForUser so a "sign out everywhere" admin action also
// kills the remember cookie path; without this hook, a revoked browser
// could resurrect via its remember cookie on the next request.
//
// Implementations must be best-effort: a failure here does not undo the
// store-side session deletion, so callers should log + continue.
//
// userID is passed as the string form (matching DeleteAllForUser);
// providers keyed by other types convert in their own FindByID.
type RememberTokenClearer interface {
	ClearRememberTokensForUser(ctx context.Context, userID string) error
}

// RefreshTokenRevoker is the optional interface a Guard implements to
// invalidate persistent refresh tokens (bearer-token / JWT auth) for a
// user. Manager.RevokeAllSessions walks every registered guard and
// invokes RevokeAllRefreshTokensForUser so a "sign out everywhere"
// admin action also kills outstanding refresh tokens; without this
// hook, a phished refresh token survives the administrative purge for
// up to RefreshTTL (default 14 days) and re-mints fresh access tokens
// for the attacker (audit M-10).
//
// Session guards do not need this interface (the cookie revocation list
// and server-side store deletion already cover their access surface);
// JWT guards do because their refresh tokens have no equivalent of a
// per-session record on the server. JWTGuard's implementation bumps
// the user's refresh-token generation counter (the same H-07 mechanism
// used on individual Logout).
//
// Implementations must be best-effort: a failure here does not undo the
// store-side session deletion, so callers should log + continue.
type RefreshTokenRevoker interface {
	RevokeAllRefreshTokensForUser(ctx context.Context, userID string) error
}

// ToMeta returns the listing-only projection of a StoredSession.
func (s *StoredSession) ToMeta() *SessionMeta {
	return &SessionMeta{
		ID:         s.ID,
		UserID:     s.UserID,
		CreatedAt:  s.CreatedAt,
		LastSeenAt: s.LastSeenAt,
		ExpiresAt:  s.ExpiresAt,
		IPAddress:  s.IPAddress,
		UserAgent:  s.UserAgent,
	}
}
