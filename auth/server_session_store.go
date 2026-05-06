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
