package mail

import (
	"errors"

	"github.com/velocitykode/velocity/contract"
)

var (
	ErrDriverNotConfigured = errors.New("velocity/mail: driver not configured")
	ErrChannelNotFound     = errors.New("velocity/mail: channel not found")

	// The following sentinels are owned by the contract leaf (the Message
	// type that references them lives there). They are re-exported here as
	// aliases so existing errors.Is(err, mail.ErrInvalidHeader) checks keep
	// matching against the shared identity.

	// ErrAttachmentTooLarge is returned when an attachment exceeds the
	// configured MaxAttachmentSize.
	ErrAttachmentTooLarge = contract.ErrAttachmentTooLarge

	// ErrInvalidHeader is returned when a header name or value, a subject,
	// or a structured address field contains forbidden control characters.
	ErrInvalidHeader = contract.ErrInvalidHeader

	// ErrAttachmentRootRequired is returned by Message.AttachFile when no
	// attachment root has been registered.
	ErrAttachmentRootRequired = contract.ErrAttachmentRootRequired

	// ErrAttachmentPathOutsideRoot is returned when the requested attachment
	// path escapes the registered attachment root.
	ErrAttachmentPathOutsideRoot = contract.ErrAttachmentPathOutsideRoot

	// ErrInvalidEmailAddress is returned by the address setters when the
	// email parameter is not a single RFC 5322 addr-spec.
	ErrInvalidEmailAddress = contract.ErrInvalidEmailAddress
)
