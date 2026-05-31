package mail

import (
	"github.com/velocitykode/velocity/contract"
)

// Message represents an email message. The canonical declaration (the struct,
// its NewMessage constructor, fluent setters, getters, attachment and template
// machinery) lives in the stdlib-only contract leaf; this alias keeps the mail
// API byte-identical for existing callers and drivers.
type Message = contract.Message

// NewMessage creates a new email message. Canonical implementation in the
// contract leaf.
var NewMessage = contract.NewMessage

// DefaultMaxAttachmentSize is the fallback per-attachment size limit. Canonical
// declaration in the contract leaf.
const DefaultMaxAttachmentSize = contract.DefaultMaxAttachmentSize

// SetTemplatePath configures the directory for email templates.
// Canonical implementation in the contract leaf. Safe for concurrent use.
var SetTemplatePath = contract.SetTemplatePath

// SetDefaultAttachmentRoot registers the process-wide attachment root used by
// Message.AttachFile. Canonical implementation in the contract leaf.
var SetDefaultAttachmentRoot = contract.SetDefaultAttachmentRoot

// GetDefaultAttachmentRoot returns the currently registered package-level
// attachment root, or nil. Canonical implementation in the contract leaf.
var GetDefaultAttachmentRoot = contract.GetDefaultAttachmentRoot

// SetDefaultMaxAttachmentSize replaces the package-level fallback used by
// NewMessage. Canonical implementation in the contract leaf.
var SetDefaultMaxAttachmentSize = contract.SetDefaultMaxAttachmentSize

// GetDefaultMaxAttachmentSize returns the current package-level limit.
// Canonical implementation in the contract leaf.
var GetDefaultMaxAttachmentSize = contract.GetDefaultMaxAttachmentSize

// IsErrAttachmentTooLarge is a convenience for errors.Is(err, ErrAttachmentTooLarge).
var IsErrAttachmentTooLarge = contract.IsErrAttachmentTooLarge

// IsErrInvalidHeader is a convenience for errors.Is(err, ErrInvalidHeader).
var IsErrInvalidHeader = contract.IsErrInvalidHeader
