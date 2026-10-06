package schemes

import (
	"fmt"

	"example.com/lockheld/auth/internal/sessionref"
	"example.com/lockheld/contract"
	"example.com/lockheld/internal/errchain"
	"example.com/lockheld/internal/fallbacklog"
)

// session is a session by shape: ID, Regenerate, Invalidate.
type session interface {
	ID() string
	Regenerate() error
	Invalidate() error
	Save() error
}

type modified interface{ IsModified() bool }

// user has an ID too, and is not a session.
type user interface{ ID() string }

// StoredSession is a session record.
type StoredSession struct {
	ID     string
	UserID string
}

type scheme struct{ logger contract.Logger }

func (g *scheme) logWarn(msg string, kvs ...any) {}

// The seam asks the mark, then saves: a save in flight has the mark
// cleared. Flagged at the read.
func commit(s session) error {
	if m, ok := s.(modified); ok && !m.IsModified() { // want mark
		return nil
	}
	return s.Save()
}

// The seam just saves.
func commitAlways(s session) error {
	return s.Save()
}

// A read that decides something else carries the marker; one without a
// rationale does not suppress; one on a line with no read is stale.
func refresh(s session, due bool) bool {
	m := s.(modified)
	if !due && !m.IsModified() { //session-mark-ok: decides only whether the record refresh runs
		return false
	}
	if m.IsModified() { // want mark //session-mark-ok:
		return true
	}
	_ = due //session-mark-ok: nothing on this line any more // want stale
	return false
}

// A method expression with its receiver passed, and a method value, read
// the mark as the call does.
func commitByExpression(s session) error {
	m := s.(modified)
	if !modified.IsModified(m) { // want mark
		return nil
	}
	read := m.IsModified // want mark
	if !read() {
		return nil
	}
	return s.Save()
}

// Fields bound to a logger are written with every line it writes after.
func (g *scheme) bound(s session, sessionID string) {
	g.logger.With("session", s.ID()).Warn("save failed")                   // want session-id
	g.logger.With("session_id", 1).Warn("save failed")                     // want session-id
	contract.BindFields(g.logger, "session", sessionID).Warn("get failed") // want session-id
	fallbacklog.Logger{}.With("session", sessionID).Warn("get failed")     // want session-id

	g.logWarn("save failed", "session", session.ID(s)) // want session-id

	g.logger.With("session", sessionref.Of(s.ID())).Warn("save failed")
	contract.BindFields(g.logger, "session", sessionref.Of(sessionID)).Warn("get failed")
}

// A logger method named through its type is the same log call, and an id
// joined into a message or an error text is the id.
func (g *scheme) joined(s session, rec *StoredSession, sessionID string) error {
	contract.Logger.Warn(g.logger, "save failed", "session", sessionID)   // want session-id
	contract.Logger.With(g.logger, "session", s.ID()).Warn("save failed") // want session-id
	g.logger.Warn("save failed for " + s.ID())                            // want session-id
	g.logWarn("save failed for " + ("session " + rec.ID) + ".")           // want session-id
	g.logger.Warn("save failed", "session", "id:"+sessionID)              // want session-id
	if rec == nil {
		return errchain.Errorf("save failed for " + s.ID()) // want session-id
	}
	if s == nil {
		return fmt.Errorf("save failed for "+sessionID+": %w", nil) // want session-id
	}

	msg := "save failed for " + s.ID()
	g.logWarn(msg) // want session-id

	contract.Logger.Warn(g.logger, "save failed", "session", sessionref.Of(sessionID))
	g.logger.Warn("save failed for " + sessionref.Of(s.ID()))
	return errchain.Errorf("save failed for " + sessionref.Of(sessionID))
}

// A session id in a log line, by key and by value.
func (g *scheme) logs(s session, rec *StoredSession, u user, sessionID, userID string) {
	g.logWarn("save failed", "session_id", sessionref.Of(s.ID())) // want session-id
	g.logWarn("save failed", "session", s.ID())                   // want session-id
	g.logWarn("touch failed", "session", rec.ID)                  // want session-id
	g.logWarn("get failed", "session", sessionID)                 // want session-id
	g.logger.Warn("get failed", "session", sessionID)             // want session-id
	fallbacklog.Logger{}.Warn("get failed", "session", (s.ID()))  // want session-id
	id := s.ID()
	g.logWarn("revoke failed", "session", id) // want session-id
	var old string
	old = rec.ID
	g.logWarn("rotate failed", "old", old) // want session-id

	// By reference, a user's id, the record's owner: not flagged.
	g.logWarn("save failed", "session", sessionref.Of(s.ID()))
	g.logWarn("save failed", "session", sessionref.Of(sessionID))
	g.logWarn("save failed", "user_id", u.ID())
	g.logWarn("save failed", "user_id", rec.UserID, "user", userID)
	g.logger.Warn("save failed", "sessions", 3)
}

// A session id built into an error text.
func errs(s session, rec *StoredSession, sessionID string, err error) error {
	if err != nil {
		return errchain.Errorf("write session %s: %w", rec.ID, err) // want session-id
	}
	if sessionID == "" {
		return fmt.Errorf("delete session %s", s.ID()) // want session-id
	}
	_ = fmt.Sprintf("session:meta:%s", sessionID)
	return errchain.Errorf("write session %s: %w", sessionref.Of(sessionID), err)
}

// A package variable holding a func literal is looked at too.
var saveSeam = func(g *scheme, s session) {
	g.logWarn("save failed", "session_id", 1) // want session-id
}
