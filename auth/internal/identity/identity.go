package identity

import (
	"errors"
	"strconv"

	"github.com/velocitykode/velocity/internal/errchain"
)

// ErrUnreadable is returned for a user whose identifier cannot be read: its
// GetAuthIdentifier panicked, or formatting the identifier did (its String,
// Error or Format method). auth re-exports it as ErrIdentifierUnreadable.
var ErrUnreadable = errors.New("velocity/auth: user identifier unreadable: GetAuthIdentifier or the identifier's String, Error or Format method panicked")

// user is the part of auth.Authenticatable Of calls.
type user interface{ GetAuthIdentifier() interface{} }

// Of returns u's identifier as GetAuthIdentifier returns it and as text
// (fmt's %v form: a string as is, an integer in decimal, a Stringer's
// String), with GetAuthIdentifier and the formatting both run contained.
// When either panics it returns ErrUnreadable and no text: the text is a
// key (a token subject, a session record's owner, a remember credential),
// so an unreadable identifier must fail the operation rather than share a
// placeholder with every other unreadable one.
func Of(u user) (id any, text string, err error) {
	defer func() {
		if recover() != nil {
			id, text, err = nil, "", ErrUnreadable
		}
	}()
	id = u.GetAuthIdentifier()
	switch v := id.(type) {
	case string:
		return id, v, nil
	case uint:
		return id, strconv.FormatUint(uint64(v), 10), nil
	case uint64:
		return id, strconv.FormatUint(v, 10), nil
	case int:
		return id, strconv.Itoa(v), nil
	case int64:
		return id, strconv.FormatInt(v, 10), nil
	}
	text, ok := errchain.ReadValue(id)
	if !ok {
		return nil, "", ErrUnreadable
	}
	return id, text, nil
}
