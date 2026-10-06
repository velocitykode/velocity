// Package session stands in for the session driver package, whose stores
// decide on the modified mark inside their own save.
package session

type Session struct{ modified bool }

func (s *Session) IsModified() bool { return s.modified }

// The store's own save reads the mark: not flagged here.
func save(s *Session) bool {
	return s.IsModified()
}
