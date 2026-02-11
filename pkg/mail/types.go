package mail

import (
	"context"
	"fmt"
	netmail "net/mail"
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

// String returns the formatted email address
func (a Address) String() string {
	if a.Name != "" {
		return a.Name + " <" + a.Email + ">"
	}
	return a.Email
}

// Attachment represents an email attachment
type Attachment struct {
	Name        string
	Data        []byte
	ContentType string
}
