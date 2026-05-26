package mail

import (
	"context"
	"fmt"
	netmail "net/mail"
	"strings"
)

// Mailer interface that all mail drivers must implement
type Mailer interface {
	Send(ctx context.Context, msg *Message) error
}

// Priority levels for emails
type Priority int

const (
	LowPriority Priority = iota
	NormalPriority
	HighPriority
)

// Address represents an email address
type Address struct {
	Email string
	Name  string
}

// NewAddress creates and validates an email address.
// Returns an error if the email format is invalid.
func NewAddress(email string, name ...string) (Address, error) {
	if _, err := netmail.ParseAddress(email); err != nil {
		return Address{}, fmt.Errorf("mail: invalid email address %q: %w", email, err)
	}
	addr := Address{Email: email}
	if len(name) > 0 {
		addr.Name = name[0]
	}
	return addr, nil
}

// String returns the formatted email address. When a display name is
// present, the result is produced by net/mail.Address.String(), which
// applies RFC 2047 / RFC 5322 phrase quoting (any specials in the name
// are quoted-string or encoded-word escaped). Hand-rolled concatenation
// is avoided so that recipients cannot be impersonated by a name like
//
//	Bob" <evil@x>, "Real
//
// being interpolated raw into a header. The Name component is separately
// rejected at the fluent setters (see validateAddressField) for the
// address-grammar specials it must not contain even after quoting.
//
// For an empty Name a "safe" addr-spec (one that parses cleanly via
// net/mail.ParseAddress and contains no list-separator or angle-bracket
// characters) is returned bare to preserve the prior log format. Anything
// else falls through to net/mail.Address{Address: a.Email}.String(),
// which wraps the addr-spec in angle brackets and applies quoted-local-
// part rules: this is defence in depth against callers that bypass the
// setter validators by constructing mail.Address{} literals directly
// (e.g. notification/notification.go), so header-split payloads in the
// Email field cannot reach the wire raw.
func (a Address) String() string {
	if a.Name == "" {
		return safeAddrSpec(a.Email)
	}
	na := netmail.Address{Name: a.Name, Address: a.Email}
	return na.String()
}

// safeAddrSpec returns email unchanged when it parses as a single bare
// addr-spec with no list-separator or angle-bracket characters; otherwise
// it returns the net/mail-quoted form which will at minimum wrap the
// address in angle brackets so it cannot split a structured header line.
func safeAddrSpec(email string) string {
	if i := strings.IndexAny(email, ",;<>"); i < 0 {
		if parsed, err := netmail.ParseAddress(email); err == nil && parsed.Name == "" {
			return email
		}
	}
	na := netmail.Address{Address: email}
	return na.String()
}

// Attachment represents an email attachment
type Attachment struct {
	Name        string
	Data        []byte
	ContentType string
}
