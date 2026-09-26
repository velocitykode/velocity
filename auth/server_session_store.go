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

	// ErrSessionExpired is returned by ServerSessionStore.Get and Touch
	// when a stored session record has passed its ExpiresAt deadline, and
	// by SessionScheme.CheckWithError when the request's signed-in session
	// ended under the lifetime policy: idle for longer than
	// SessionConfig.IdleLifetime, or older than
	// SessionConfig.AbsoluteLifetime, on the cookie or the server record.
	ErrSessionExpired = errors.New("velocity/auth: session expired")

	// ErrSessionRevoked is returned by SessionScheme.CheckWithError when
	// the request carries a live cookie but the corresponding server-side
	// session record has been deleted (e.g. via Manager.RevokeSession or
	// RevokeAllSessions). Distinct from ErrSessionExpired: expired means
	// the lifetime policy ended the session; revoked means an
	// administrative action removed it while the cookie was still live.
	ErrSessionRevoked = errors.New("velocity/auth: session revoked")

	// ErrNoServerSessionStore is returned by Manager.RevokeSession,
	// RevokeAllSessions, and ListActiveSessions when no server-side
	// session store has been installed via SetServerSessionStore.
	ErrNoServerSessionStore = errors.New("velocity/auth: no server session store configured")
)

// StoredSession is the server-side persisted view of a session: one record
// per session, which is both the revocation index entry and, with
// session.ServerStore, the home of the session's data.
type StoredSession struct {
	// ID is the opaque session identifier.
	ID string
	// UserID is the authenticated user identifier (string form for
	// driver portability). Empty for a signed-out visitor's session,
	// which only session.ServerStore writes: such a record is never
	// indexed, listed or removed by DeleteAllForUser.
	UserID string
	// Data carries the session's payload when session.ServerStore holds
	// the session (nil when the payload lives in the cookie); drivers must
	// accept any JSON-shaped tree.
	Data map[string]any
	// CreatedAt records when the session was first written.
	CreatedAt time.Time
	// LastSeenAt records the most recent access (drivers should refresh
	// this on Put).
	LastSeenAt time.Time
	// ExpiresAt is when the record ends: the session scheme sets it from
	// the lifetime policy (SessionConfig.ExpiresAt) on Put and slides it on
	// every Touch. Records past it are treated as ErrSessionExpired by Get
	// and Touch and reaped by background sweeps. Zero means no expiry.
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
// session records on the server, one record per session. It underwrites
// administrative operations like "log out every device" and "list my
// active sessions", and session.ServerStore (a SessionStore) keeps the
// session's data in the same record, so the cookie carries only the id.
// Implementations must be safe for concurrent use.
//
// Implementations must pass authtest.RunServerSessionStoreContractTests.
// See authtest for the executable specification.
type ServerSessionStore interface {
	// Get returns the StoredSession for id. Returns ErrSessionNotFound
	// when no record exists; returns ErrSessionExpired (and removes the
	// record) when the record has passed ExpiresAt.
	Get(ctx context.Context, id string) (*StoredSession, error)

	// Put creates or replaces a session record. Implementations must
	// update LastSeenAt to time.Now() and reject records with an empty ID.
	// An empty UserID is a signed-out visitor's record: it is stored and
	// returned by Get like any other, but never indexed for the user
	// operations (ListForUser, DeleteAllForUser).
	//
	// Put is the create write only: the sign-in record, and the first save
	// of a signed-out visitor's session. It must never be used to refresh
	// or update an existing record: a create-or-replace issued after a
	// concurrent Delete would resurrect a revoked session. Use Touch and
	// UpdateData for that.
	Put(ctx context.Context, session *StoredSession) error

	// UpdateData replaces the record's Data and slides it like Touch
	// (LastSeenAt to lastSeen, ExpiresAt to expiresAt), leaving every
	// other field as it is. session.ServerStore saves the session through
	// it. Like Touch it is update-if-present: ErrSessionNotFound when no
	// record exists for id (never an insert), ErrSessionExpired (and the
	// record removed) when the record has passed its current ExpiresAt.
	UpdateData(ctx context.Context, id string, data map[string]any, lastSeen, expiresAt time.Time) error

	// Touch is the activity refresh: it sets LastSeenAt to lastSeen and
	// ExpiresAt to expiresAt on an existing record, so an active session's
	// record slides with its idle window (the scheme computes expiresAt
	// from the lifetime policy, capped at the absolute lifetime). A backend
	// with its own record TTL must extend it to expiresAt. Touch is
	// update-if-present: it returns ErrSessionNotFound when no record
	// exists for id and must never insert one, so a refresh racing a
	// revocation cannot recreate the deleted row. It returns
	// ErrSessionExpired (and removes the record) when the record has
	// already passed its current ExpiresAt; an expired session is never
	// revived by a Touch.
	Touch(ctx context.Context, id string, lastSeen, expiresAt time.Time) error

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

// ServerSessionStoreReceiver is the optional interface a Scheme implements
// to opt into server-side session revocation. Manager.SetServerSessionStore
// walks all registered schemes and propagates the store to every scheme that
// implements this interface; schemes that do not implement it (e.g. JWT) are
// skipped without error.
type ServerSessionStoreReceiver interface {
	SetServerSessionStore(store ServerSessionStore)
}

// RememberTokenClearer is the optional interface a Scheme implements to
// invalidate persistent "remember me" credentials for a user.
// Manager.RevokeAllSessions walks every registered scheme and invokes
// ClearRememberTokensForUser so a "sign out everywhere" admin action also
// kills the remember cookie path; without this hook, a revoked browser
// could resurrect via its remember cookie on the next request.
//
// Implementations must be best-effort: a failure here does not undo the
// store-side session deletion, so callers should log + continue.
//
// userID is passed as the string form (matching DeleteAllForUser);
// user stores keyed by other types convert in their own FindByID.
type RememberTokenClearer interface {
	ClearRememberTokensForUser(ctx context.Context, userID string) error
}

// RefreshTokenRevoker is the optional interface a Scheme implements to
// invalidate persistent refresh tokens (bearer-token / JWT auth) for a
// user. Manager.RevokeAllSessions walks every registered scheme and
// invokes RevokeAllRefreshTokensForUser so a "sign out everywhere"
// admin action also kills outstanding refresh tokens; without this
// hook, a phished refresh token survives the administrative purge for
// up to RefreshTTL (default 14 days) and re-mints fresh access tokens
// for the attacker (audit M-10).
//
// Session schemes do not need this interface (the cookie revocation list
// and server-side store deletion already cover their access surface);
// JWT schemes do because their refresh tokens have no equivalent of a
// per-session record on the server. JWTScheme's implementation bumps
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
