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

// Address represents an email address.
//
// Direct struct-literal construction (`mail.Address{Email: x, Name: y}`)
// is DEPRECATED in favour of NewAddress, which validates the email and
// rejects header-injection payloads up front. Literal construction is
// retained for backward compatibility, but every driver-side serialiser
// calls Address.Validate (defence in depth) so a literal-constructed
// address carrying CR/LF in either field is rejected before it can
// reach the wire. Prefer NewAddress in new code.
type Address struct {
	Email string
	Name  string
}

// NewAddress creates and validates an email address. The email must
// parse as a single RFC 5322 addr-spec via net/mail.ParseAddress and
// must not contain CR, LF, NUL, or any other C0 control byte (those
// would enable SMTP header injection). The optional display name is
// rejected for the same control bytes; address-grammar specials in
// the name are NOT blocked here (NewAddress is a building block, not
// a fluent setter), but Message.From/To/Cc/Bcc/ReplyTo apply the
// stricter name check.
func NewAddress(email string, name ...string) (Address, error) {
	if _, err := netmail.ParseAddress(email); err != nil {
		return Address{}, fmt.Errorf("mail: invalid email address %q: %w", email, err)
	}
	addr := Address{Email: email}
	if len(name) > 0 {
		addr.Name = name[0]
	}
	if err := addr.Validate(); err != nil {
		return Address{}, err
	}
	return addr, nil
}

// Validate enforces the same invariants on a literal-built Address
// that the fluent setters on Message (From/To/Cc/Bcc/ReplyTo) enforce
// at construction time. Drivers call Validate before serialising so a
// caller that bypasses the setters by building mail.Address{} directly
// cannot smuggle a recipient list, an embedded display name, or a
// header-split payload onto the wire.
//
// Enforced:
//
//   - Email and Name contain no CR, LF, NUL, or other C0 control byte
//     (CRLF header injection defence).
//   - Email parses as exactly one RFC 5322 addr-spec via
//     net/mail.ParseAddress, with no embedded display name and no
//     list-separator ( `,` `;` ) or angle-bracket characters. This is
//     the single-mailbox invariant: a payload like
//     "victim@example.com, attacker@evil.com" otherwise lands as two
//     recipients on Mailgun / Postmark / SMTP because their REST
//     bodies and headers are list-aware.
//   - Name (when present) carries no address-grammar special
//     ( `<>,;:"\()` ) since downstream RFC 2047 / 5322 quoting alone
//     does not prevent recipient-impersonation payloads such as
//     `Bob" <evil@x>, "Real` leaking through gateways that bypass
//     quoting.
//
// Validate's contract matches NewAddress and validateAddressField; the
// three converge on the same accept/reject set so the driver boundary
// does not see addresses the setter path would have rejected.
func (a Address) Validate() error {
	if err := validateAddrSpec(a.Email); err != nil {
		return err
	}
	if a.Name != "" {
		if containsForbiddenControl(a.Name) {
			return fmt.Errorf("%w: address Name contains CR/LF or other control characters", ErrInvalidHeader)
		}
		if i := strings.IndexAny(a.Name, "<>,;:\"\\()"); i >= 0 {
			return fmt.Errorf("%w: address Name contains address-grammar special %q",
				ErrInvalidHeader, a.Name[i])
		}
	}
	return nil
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

// validateAddrSpec enforces the single-bare-addr-spec invariant on an
// Address.Email value: no control bytes, no list-separator
// ( `,` `;` ) or angle-bracket characters, must parse as exactly one
// RFC 5322 addr-spec via net/mail.ParseAddress, and must not carry an
// embedded display name. Returns ErrInvalidHeader for control-byte
// failures and ErrInvalidEmailAddress for parse / shape failures so
// callers can distinguish header-split from list-smuggling at the
// errors.Is layer.
//
// An empty Email is accepted: presence is a driver-level concern
// ("From is required" errors come from the driver, not from this
// invariant gate). Validate only blocks smuggling; the driver still
// decides whether it can serialise a zero Address. Shared between
// Address.Validate (driver boundary, literal-built addresses) and the
// fluent setter path so both call sites converge.
func validateAddrSpec(email string) error {
	if email == "" {
		return nil
	}
	if containsForbiddenControl(email) {
		return fmt.Errorf("%w: address Email contains CR/LF or other control characters", ErrInvalidHeader)
	}
	if i := strings.IndexAny(email, ",;<>"); i >= 0 {
		return fmt.Errorf("%w: address Email contains forbidden character %q",
			ErrInvalidEmailAddress, email[i])
	}
	parsed, err := netmail.ParseAddress(email)
	if err != nil {
		return fmt.Errorf("%w: address Email %q: %v",
			ErrInvalidEmailAddress, email, err)
	}
	if parsed.Name != "" {
		return fmt.Errorf("%w: address Email %q includes a display name; use the Name field",
			ErrInvalidEmailAddress, email)
	}
	return nil
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
