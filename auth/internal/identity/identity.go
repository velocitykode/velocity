package identity

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/velocitykode/velocity/internal/errchain"
)

// ErrUnreadable is returned for a user whose identifier cannot be read: its
// GetAuthIdentifier panicked, or formatting the identifier did (its String,
// Error or Format method). auth re-exports it as ErrIdentifierUnreadable.
var ErrUnreadable = errors.New("velocity/auth: user identifier unreadable: GetAuthIdentifier or the identifier's String, Error or Format method panicked")

// user is the part of contract.Authenticatable Of calls.
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

// Snapshot returns u's identifier in a form that cannot change after the
// call, with its text as Of gives it. A string or an int, int64, uint or
// uint64 is returned as it is: it is a value. Any other identifier is
// encoded to JSON here, once, and returned as a json.RawMessage, which
// marshals as those bytes: the identifier object, its MarshalJSON
// included, is not called again when the snapshot is later signed into a
// token. Both forms are read in this one call, so a token's subject and
// its user id claim name the same identity even when application code
// changes the identifier object afterwards.
//
// The encoding runs contained like the reads in Of: an identifier whose
// MarshalJSON panics or fails is unreadable (ErrUnreadable, and the
// encoder's error when there is one).
func Snapshot(u user) (id any, text string, err error) {
	id, text, err = Of(u)
	if err != nil {
		return nil, "", err
	}
	switch id.(type) {
	case string, int, int64, uint, uint64:
		return id, text, nil
	}
	defer func() {
		if recover() != nil {
			id, text, err = nil, "", ErrUnreadable
		}
	}()
	encoded, mErr := json.Marshal(id)
	if mErr != nil {
		return nil, "", errchain.Errorf("%w: %w", ErrUnreadable, mErr)
	}
	return json.RawMessage(encoded), text, nil
}
