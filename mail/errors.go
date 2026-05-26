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
	// forbidden characters, in particular CR (\r), LF (\n) or other
	// C0 control characters that would enable SMTP header injection.
	ErrInvalidHeader = errors.New("velocity/mail: header contains forbidden control characters")

	// ErrAttachmentRootRequired is returned by Message.AttachFile when no
	// attachment root has been registered (neither per-message via
	// WithAttachmentRoot nor package-wide via SetDefaultAttachmentRoot).
	// AttachFile is no longer a free pass to read arbitrary absolute
	// paths: applications must explicitly opt in to a containment root,
	// matching the rule that filesystem accesses are sandboxed by the
	// kernel via os.Root.
	ErrAttachmentRootRequired = errors.New("velocity/mail: AttachFile requires a registered attachment root (call SetDefaultAttachmentRoot or Message.WithAttachmentRoot)")

	// ErrAttachmentPathOutsideRoot is returned when the requested
	// attachment path escapes the registered attachment root through
	// traversal sequences or symlink targets. Containment is enforced by
	// (*os.Root).Open, which on Linux uses openat2 with
	// RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS and on other platforms uses the
	// strongest equivalent the runtime can provide.
	ErrAttachmentPathOutsideRoot = errors.New("velocity/mail: attachment path escapes attachment root")

	// ErrInvalidEmailAddress is returned by the address setters (From/To/
	// Cc/Bcc/ReplyTo) when the email parameter is not a single RFC 5322
	// addr-spec. The setters reject:
	//
	//   - comma- or semicolon-separated lists (the SMTP list separator
	//     would split the rendered header into multiple mailboxes, with
	//     an attacker's address smuggled in alongside the legitimate
	//     recipient),
	//   - display-name forms like "Bob <bob@x>" passed in the email
	//     parameter (display names must come via the Name argument, so
	//     the setter can validate them separately),
	//   - inputs that net/mail.ParseAddress cannot resolve to exactly
	//     one mailbox,
	//   - inputs containing the address-grammar specials <>,; even when
	//     ParseAddress happens to swallow them (defence in depth
	//     against parser ambiguity).
	ErrInvalidEmailAddress = errors.New("velocity/mail: invalid email address")
)
