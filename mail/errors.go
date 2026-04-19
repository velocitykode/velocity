package mail

import "errors"

var (
	ErrDriverNotConfigured = errors.New("velocity/mail: driver not configured")
	ErrChannelNotFound     = errors.New("velocity/mail: channel not found")

	// ErrAttachmentTooLarge is returned when an attachment exceeds the
	// configured MaxAttachmentSize. Callers must keep attachments within the
	// limit to avoid OOM on malicious or misconfigured input.
	ErrAttachmentTooLarge = errors.New("velocity/mail: attachment exceeds maximum allowed size")

	// ErrInvalidHeader is returned when a header name or value, a subject,
	// or a structured address field (From/To/Cc/Bcc/ReplyTo) contains
	// forbidden characters — in particular CR (\r), LF (\n) or other
	// C0 control characters that would enable SMTP header injection.
	ErrInvalidHeader = errors.New("velocity/mail: header contains forbidden control characters")
)
