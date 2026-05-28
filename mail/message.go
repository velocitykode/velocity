package mail

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	netmail "net/mail"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// DefaultMaxAttachmentSize is the fallback per-attachment size limit used
// when neither the MailConfig nor the *Message specifies one. 25 MiB matches
// the common sweet spot across SMTP providers (SES 40, SendGrid 30,
// Postmark 10, Mailgun 25) and prevents trivial OOM-via-attachment DoS.
const DefaultMaxAttachmentSize int64 = 25 * 1024 * 1024

// defaultTemplatePath is the initial directory where email templates
// live, before any SetTemplatePath call. Treated as an immutable
// constant; the live value is stored in templatePathStore.
const defaultTemplatePath = "resources/views/emails"

// templatePathStore is the live template directory. Reads happen from
// Message.Template; writes happen via SetTemplatePath. Strings in Go
// are two-word headers (data pointer + length); a concurrent read and
// write of a bare package-level string can observe a torn header and
// produce a corrupt path. atomic.Value gives us race-clean load/store
// semantics without the lock overhead a sync.RWMutex would impose on
// every Template() call.
var templatePathStore atomic.Value // string

func init() {
	templatePathStore.Store(defaultTemplatePath)
}

// SetTemplatePath configures the directory for email templates.
// Safe for concurrent use.
func SetTemplatePath(path string) {
	templatePathStore.Store(path)
}

// templatePath returns the live template directory. atomic.Value
// guarantees the returned string is a non-torn snapshot.
func templatePath() string {
	if v, ok := templatePathStore.Load().(string); ok {
		return v
	}
	return defaultTemplatePath
}

// defaultMaxAttachmentSize is the package-level fallback used by NewMessage
// when no explicit limit is supplied. NewMailer promotes MailConfig values
// into it at boot so freshly constructed messages inherit the app config.
var (
	defaultMaxAttachmentSize   = DefaultMaxAttachmentSize
	defaultMaxAttachmentSizeMu sync.RWMutex
)

// defaultAttachmentRoot is the package-level attachment containment root used
// by Message.AttachFile when the message does not specify its own root via
// WithAttachmentRoot. Reads and writes are guarded by defaultAttachmentRootMu
// so boot-time registration races with in-flight sends are safe (string
// fields under concurrent write are not torn-write safe in Go; *os.Root
// would be the same hazard absent the mutex).
//
// A nil value means "no root configured": AttachFile returns
// ErrAttachmentRootRequired rather than silently reading arbitrary paths.
var (
	defaultAttachmentRoot   *os.Root
	defaultAttachmentRootMu sync.RWMutex
)

// SetDefaultAttachmentRoot registers the process-wide attachment root used by
// Message.AttachFile when no per-message root is set. Pass nil to unregister
// (subsequent AttachFile calls without a per-message root return
// ErrAttachmentRootRequired). The caller retains ownership of the *os.Root
// and is responsible for closing it at shutdown.
//
// Typical usage at boot:
//
//	root, err := os.OpenRoot("/srv/app/attachments")
//	if err != nil { ... }
//	mail.SetDefaultAttachmentRoot(root)
//
// Safe for concurrent use.
func SetDefaultAttachmentRoot(root *os.Root) {
	defaultAttachmentRootMu.Lock()
	defer defaultAttachmentRootMu.Unlock()
	defaultAttachmentRoot = root
}

// GetDefaultAttachmentRoot returns the currently registered package-level
// attachment root, or nil if none has been configured.
func GetDefaultAttachmentRoot() *os.Root {
	defaultAttachmentRootMu.RLock()
	defer defaultAttachmentRootMu.RUnlock()
	return defaultAttachmentRoot
}

// SetDefaultMaxAttachmentSize replaces the package-level fallback used by
// NewMessage. A zero or negative value resets to DefaultMaxAttachmentSize.
// Safe for concurrent use; intended to be called once during boot.
func SetDefaultMaxAttachmentSize(n int64) {
	defaultMaxAttachmentSizeMu.Lock()
	defer defaultMaxAttachmentSizeMu.Unlock()
	if n <= 0 {
		defaultMaxAttachmentSize = DefaultMaxAttachmentSize
		return
	}
	defaultMaxAttachmentSize = n
}

// GetDefaultMaxAttachmentSize returns the current package-level limit.
func GetDefaultMaxAttachmentSize() int64 {
	defaultMaxAttachmentSizeMu.RLock()
	defer defaultMaxAttachmentSizeMu.RUnlock()
	return defaultMaxAttachmentSize
}

// Message represents an email message
type Message struct {
	from        Address
	to          []Address
	cc          []Address
	bcc         []Address
	replyTo     []Address
	subject     string
	textBody    string
	htmlBody    string
	attachments []Attachment
	headers     map[string]string
	priority    Priority

	// maxAttachmentSize is the per-attachment byte limit enforced by
	// AttachFile and AttachData. Zero means "use the package default".
	maxAttachmentSize int64

	// attachmentRoot is the optional per-message containment root used by
	// AttachFile. Nil means "fall back to the package-level default" (see
	// SetDefaultAttachmentRoot). If both are nil, AttachFile rejects with
	// ErrAttachmentRootRequired.
	attachmentRoot *os.Root

	// err is the first error accumulated by a fluent setter (size-limit
	// violation, CRLF injection attempt, ...). It is returned by Err() and
	// surfaced by Manager.Send / checkedMailer.Send before any driver sees
	// the message. Once set, subsequent setters skip their mutation.
	err error
}

// NewMessage creates a new email message. Its attachment size limit is
// initialised from the package-level default (see SetDefaultMaxAttachmentSize).
func NewMessage() *Message {
	return &Message{
		to:                make([]Address, 0),
		cc:                make([]Address, 0),
		bcc:               make([]Address, 0),
		replyTo:           make([]Address, 0),
		attachments:       make([]Attachment, 0),
		headers:           make(map[string]string),
		priority:          NormalPriority,
		maxAttachmentSize: GetDefaultMaxAttachmentSize(),
	}
}

// WithAttachmentRoot registers a per-message attachment containment root.
// AttachFile will resolve its path argument against this root via
// (*os.Root).Open, which is symlink-safe and cannot be escaped by ".."
// segments. Passing nil clears the per-message root, falling back to the
// package-level default set by SetDefaultAttachmentRoot.
//
// The caller retains ownership of the *os.Root and is responsible for
// closing it; the Message does not.
func (m *Message) WithAttachmentRoot(root *os.Root) *Message {
	m.attachmentRoot = root
	return m
}

// WithMaxAttachmentSize overrides the per-attachment size limit for this
// message. A zero or negative value resets to the package default. Must be
// called BEFORE AttachFile / AttachData — prior attachments are not
// retroactively re-validated.
func (m *Message) WithMaxAttachmentSize(n int64) *Message {
	if n <= 0 {
		m.maxAttachmentSize = GetDefaultMaxAttachmentSize()
	} else {
		m.maxAttachmentSize = n
	}
	return m
}

// MaxAttachmentSize returns the effective per-attachment size limit.
func (m *Message) MaxAttachmentSize() int64 {
	if m.maxAttachmentSize <= 0 {
		return GetDefaultMaxAttachmentSize()
	}
	return m.maxAttachmentSize
}

// Err returns the first deferred error accumulated by any fluent setter on
// this message (CRLF injection, oversized attachment, ...). It is checked by
// Manager.Send and the checkedMailer wrapper returned from NewMailer, so
// messages with setter errors are rejected before reaching the driver even
// when the caller ignores the individual fluent return values.
func (m *Message) Err() error { return m.err }

// setErr records the first error observed. Subsequent errors are ignored —
// the caller gets the original root cause rather than an avalanche.
func (m *Message) setErr(err error) {
	if m.err == nil && err != nil {
		m.err = err
	}
}

// From sets the sender address
func (m *Message) From(email string, name ...string) *Message {
	if m.err != nil {
		return m
	}
	var n string
	if len(name) > 0 {
		n = name[0]
	}
	if err := validateAddressField("From", email, n); err != nil {
		m.setErr(err)
		return m
	}
	m.from = Address{Email: email, Name: n}
	return m
}

// To adds a recipient
func (m *Message) To(email string, name ...string) *Message {
	if m.err != nil {
		return m
	}
	var n string
	if len(name) > 0 {
		n = name[0]
	}
	if err := validateAddressField("To", email, n); err != nil {
		m.setErr(err)
		return m
	}
	m.to = append(m.to, Address{Email: email, Name: n})
	return m
}

// CC adds a CC recipient
func (m *Message) CC(email string, name ...string) *Message {
	if m.err != nil {
		return m
	}
	var n string
	if len(name) > 0 {
		n = name[0]
	}
	if err := validateAddressField("Cc", email, n); err != nil {
		m.setErr(err)
		return m
	}
	m.cc = append(m.cc, Address{Email: email, Name: n})
	return m
}

// BCC adds a BCC recipient
func (m *Message) BCC(email string, name ...string) *Message {
	if m.err != nil {
		return m
	}
	var n string
	if len(name) > 0 {
		n = name[0]
	}
	if err := validateAddressField("Bcc", email, n); err != nil {
		m.setErr(err)
		return m
	}
	m.bcc = append(m.bcc, Address{Email: email, Name: n})
	return m
}

// ReplyTo sets the reply-to address
func (m *Message) ReplyTo(email string, name ...string) *Message {
	if m.err != nil {
		return m
	}
	var n string
	if len(name) > 0 {
		n = name[0]
	}
	if err := validateAddressField("Reply-To", email, n); err != nil {
		m.setErr(err)
		return m
	}
	m.replyTo = append(m.replyTo, Address{Email: email, Name: n})
	return m
}

// Subject sets the email subject
func (m *Message) Subject(subject string) *Message {
	if m.err != nil {
		return m
	}
	if err := validateHeaderValue("Subject", subject); err != nil {
		m.setErr(err)
		return m
	}
	m.subject = subject
	return m
}

// Body sets the plain text body (convenience method)
func (m *Message) Body(body string) *Message {
	m.textBody = body
	return m
}

// TextBody sets the plain text body
func (m *Message) TextBody(body string) *Message {
	m.textBody = body
	return m
}

// HTMLBody sets the HTML body
func (m *Message) HTMLBody(body string) *Message {
	m.htmlBody = body
	return m
}

// AttachFile attaches a file resolved against the configured attachment
// root. A root MUST be registered before calling AttachFile: either
// per-message via WithAttachmentRoot or process-wide via
// SetDefaultAttachmentRoot. Calls without a configured root return
// ErrAttachmentRootRequired. This is a deliberate breaking change for
// safety: the previous implementation accepted any absolute path the
// process could read (local-file-read primitive) and relied on a weak
// strings.Contains(p, "..") check that did not defeat symlink escapes.
//
// Containment is enforced by (*os.Root).Open, which on Linux uses
// openat2 with RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS and on other
// platforms uses the strongest equivalent the runtime provides. A path
// that escapes the root via traversal or symlink targets is rejected
// with ErrAttachmentPathOutsideRoot.
//
// Returns ErrAttachmentTooLarge if the file exceeds the message's
// MaxAttachmentSize. On any failure the message also carries the error
// via Err() so a missed error return at call time is caught later by
// Send.
func (m *Message) AttachFile(path string) (*Message, error) {
	if m.err != nil {
		return m, m.err
	}

	root := m.attachmentRoot
	if root == nil {
		root = GetDefaultAttachmentRoot()
	}
	if root == nil {
		m.setErr(ErrAttachmentRootRequired)
		return m, ErrAttachmentRootRequired
	}

	// Reject absolute paths up front: (*os.Root).Open rejects them too,
	// but a dedicated message helps callers see the API mismatch faster
	// than a syscall-level error from the kernel.
	if filepath.IsAbs(path) {
		wrapped := fmt.Errorf("mail: attachment path %q must be relative to the attachment root: %w",
			path, ErrAttachmentPathOutsideRoot)
		m.setErr(wrapped)
		return m, wrapped
	}

	limit := m.MaxAttachmentSize()

	// Open via the registered root. *os.Root refuses traversal segments
	// and symlinks that target outside the root, so the previous
	// strings.Contains heuristic and the Stat-then-Open TOCTOU window
	// both disappear in a single call.
	f, err := root.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			wrapped := fmt.Errorf("mail: failed to open attachment %q: %w", path, err)
			m.setErr(wrapped)
			return m, wrapped
		}
		wrapped := fmt.Errorf("mail: attachment %q escapes attachment root: %w",
			path, errors.Join(ErrAttachmentPathOutsideRoot, err))
		m.setErr(wrapped)
		return m, wrapped
	}
	defer f.Close()

	// Stat the open handle (NOT the original path) so the size check
	// runs against the file we are about to read, eliminating a stat /
	// open TOCTOU.
	info, err := f.Stat()
	if err != nil {
		wrapped := fmt.Errorf("mail: failed to stat attachment %q: %w", path, err)
		m.setErr(wrapped)
		return m, wrapped
	}
	if info.Size() > limit {
		wrapped := fmt.Errorf("mail: attachment %q is %d bytes, limit is %d: %w",
			path, info.Size(), limit, ErrAttachmentTooLarge)
		m.setErr(wrapped)
		return m, wrapped
	}

	// Read at most limit+1 bytes so a file that grew after Stat still trips
	// the size check rather than being silently truncated or consuming
	// unbounded memory.
	data, err := io.ReadAll(io.LimitReader(f, limit+1)) //nolint:forbidigo // bounded by io.LimitReader above
	if err != nil {
		wrapped := fmt.Errorf("mail: failed to read attachment %q: %w", path, err)
		m.setErr(wrapped)
		return m, wrapped
	}
	if int64(len(data)) > limit {
		wrapped := fmt.Errorf("mail: attachment %q exceeds limit of %d bytes: %w",
			path, limit, ErrAttachmentTooLarge)
		m.setErr(wrapped)
		return m, wrapped
	}

	contentType := mime.TypeByExtension(filepath.Ext(path))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	m.attachments = append(m.attachments, Attachment{
		Name:        filepath.Base(path),
		Data:        data,
		ContentType: contentType,
	})

	return m, nil
}

// AttachData attaches data from memory. If len(data) exceeds the message's
// MaxAttachmentSize the attachment is dropped and the message carries
// ErrAttachmentTooLarge via Err(), which Send will surface.
func (m *Message) AttachData(data []byte, name, contentType string) *Message {
	if m.err != nil {
		return m
	}
	limit := m.MaxAttachmentSize()
	if int64(len(data)) > limit {
		m.setErr(fmt.Errorf("mail: in-memory attachment %q is %d bytes, limit is %d: %w",
			name, len(data), limit, ErrAttachmentTooLarge))
		return m
	}
	m.attachments = append(m.attachments, Attachment{
		Name:        name,
		Data:        data,
		ContentType: contentType,
	})
	return m
}

// Template renders a template with data and sets it as HTML body.
// Returns an error if the template name is invalid or template processing fails.
func (m *Message) Template(name string, data interface{}) (*Message, error) {
	if m.err != nil {
		return m, m.err
	}
	// Validate template name: reject path separators and traversal sequences.
	if strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
		err := fmt.Errorf("mail: invalid template name %q: must not contain path separators or traversal sequences", name)
		m.setErr(err)
		return m, err
	}

	// Snapshot once so a concurrent SetTemplatePath between Join and
	// the HasPrefix verification cannot move the goalposts.
	base := templatePath()
	tmplFile := filepath.Join(base, name+".html")
	// Verify the resolved path stays within base.
	cleanBase := filepath.Clean(base) + string(filepath.Separator)
	cleanFile := filepath.Clean(tmplFile)
	if !strings.HasPrefix(cleanFile, cleanBase) {
		err := fmt.Errorf("mail: template path traversal detected")
		m.setErr(err)
		return m, err
	}

	tmpl, err := template.ParseFiles(cleanFile)
	if err != nil {
		wrapped := fmt.Errorf("mail: failed to parse template %q: %w", name, err)
		m.setErr(wrapped)
		return m, wrapped
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		wrapped := fmt.Errorf("mail: failed to execute template %q: %w", name, err)
		m.setErr(wrapped)
		return m, wrapped
	}

	m.htmlBody = buf.String()
	return m, nil
}

// Header adds a custom header. Both key and value are validated: the key must
// be a legal RFC 5322 header token (no CTLs, ':', or whitespace) and the
// value must not contain CR or LF. Violations are stored as a deferred error
// on the message (see Err) rather than breaking the fluent signature; Send
// rejects the message before it leaves the process.
func (m *Message) Header(key, value string) *Message {
	if m.err != nil {
		return m
	}
	if err := validateHeaderName(key); err != nil {
		m.setErr(err)
		return m
	}
	if err := validateHeaderValue(key, value); err != nil {
		m.setErr(err)
		return m
	}
	m.headers[key] = value
	return m
}

// Priority sets the email priority
func (m *Message) Priority(priority Priority) *Message {
	m.priority = priority
	return m
}

// --- validation helpers (unexported) -----------------------------------------

// containsForbiddenControl reports whether s contains CR, LF, NUL, or any
// other C0 control byte (or DEL) that would enable SMTP header injection or
// split the message body. HTAB (\t) is allowed because it is legal inside
// structured header values.
func containsForbiddenControl(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\r' || c == '\n' || c == 0x00 {
			return true
		}
		if c < 0x20 && c != '\t' {
			return true
		}
		if c == 0x7f {
			return true
		}
	}
	return false
}

// validateHeaderName rejects header names that contain CR, LF, ':' (which
// would terminate the name), whitespace, or any control character. RFC 5322
// §3.2.3 restricts field-name to visible printable ASCII minus colon.
func validateHeaderName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty header name", ErrInvalidHeader)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '\r' || c == '\n' || c == '\t' || c == ':' || c == ' ' {
			return fmt.Errorf("%w: header name %q contains forbidden character", ErrInvalidHeader, name)
		}
		// RFC 5322 token: visible printable ASCII (33..126) minus ':'.
		if c < 33 || c > 126 {
			return fmt.Errorf("%w: header name %q contains non-ASCII or control byte", ErrInvalidHeader, name)
		}
	}
	return nil
}

// validateHeaderValue rejects CR, LF, NUL and other C0 controls in header
// values to block SMTP header injection. We do NOT implement RFC 5322 folding
// (CRLF + WSP) — callers that need long headers should split them into
// separate fields. This is the conservative stance recommended for 1.0.
func validateHeaderValue(field, value string) error {
	if containsForbiddenControl(value) {
		return fmt.Errorf("%w: %s value contains CR/LF or other control characters", ErrInvalidHeader, field)
	}
	return nil
}

// validateAddressField validates the email and display-name components of a
// structured address setter (From/To/Cc/Bcc/ReplyTo).
//
// The email must parse as exactly one RFC 5322 addr-spec via
// net/mail.ParseAddress, with no embedded display name and no
// list-separator (`,;`) or angle-bracket characters anywhere in the input.
// This closes a header-split primitive that the CR/LF and display-name
// checks alone missed:
//
//	To("victim@example.com, attacker@evil.com")
//
// had no CR/LF, no display name, and serialised on the wire as
// `To: victim@example.com, attacker@evil.com`, two mailboxes, the
// attacker's smuggled in alongside the victim.
//
// The display name is independently rejected if it contains CR/LF/NUL
// or any RFC 5322 address-grammar special (`<>,;:"\()`); even when
// downstream serialisers apply RFC 2047/5322 quoting, allowing these
// characters invites recipient-impersonation payloads such as
// `Bob" <attacker@evil.com>, "Real` slipping into logs or third-party
// gateways that bypass quoting. Legitimate Unicode display names are
// unaffected.
func validateAddressField(field, email, name string) error {
	if err := validateHeaderValue(field+" address", email); err != nil {
		return err
	}
	if err := validateSingleAddrSpec(field, email); err != nil {
		return err
	}
	if name != "" {
		if err := validateHeaderValue(field+" name", name); err != nil {
			return err
		}
		if i := strings.IndexAny(name, "<>,;:\"\\()"); i >= 0 {
			return fmt.Errorf("%w: %s name contains address-grammar special %q",
				ErrInvalidHeader, field, name[i])
		}
	}
	return nil
}

// validateSingleAddrSpec parses email via net/mail and rejects anything
// that is not a single bare addr-spec. Callers that want a display name
// must pass it via the Name argument of the setter so it can be validated
// separately; smuggling a display-name form ("Bob <bob@x>") through the
// email parameter is refused. List separators (`,;`) and angle brackets
// are refused outright as a belt-and-braces measure against parser
// ambiguity, before ParseAddress would have an opinion.
func validateSingleAddrSpec(field, email string) error {
	// Reject list-separator and angle-bracket characters before parsing.
	// ParseAddress also rejects most of these, but doing it here gives
	// callers a single, consistent error and closes the corner case
	// "<addr@x>" which ParseAddress accepts with an empty Name.
	if i := strings.IndexAny(email, ",;<>"); i >= 0 {
		return fmt.Errorf("%w: %s address contains forbidden character %q",
			ErrInvalidEmailAddress, field, email[i])
	}
	parsed, err := netmail.ParseAddress(email)
	if err != nil {
		return fmt.Errorf("%w: %s address %q: %v",
			ErrInvalidEmailAddress, field, email, err)
	}
	// A non-empty Name means the caller passed "Display <addr>" through
	// the email parameter. Display names must come via the Name argument
	// so they get the dedicated grammar-specials check above.
	if parsed.Name != "" {
		return fmt.Errorf("%w: %s address %q includes a display name; pass it via the name argument",
			ErrInvalidEmailAddress, field, email)
	}
	return nil
}

// IsErrAttachmentTooLarge is a convenience for errors.Is(err, ErrAttachmentTooLarge).
func IsErrAttachmentTooLarge(err error) bool { return errors.Is(err, ErrAttachmentTooLarge) }

// IsErrInvalidHeader is a convenience for errors.Is(err, ErrInvalidHeader).
func IsErrInvalidHeader(err error) bool { return errors.Is(err, ErrInvalidHeader) }

// Getters for driver access

// GetFrom returns the from address
func (m *Message) GetFrom() Address {
	return m.from
}

// GetTo returns the to addresses
func (m *Message) GetTo() []Address {
	return m.to
}

// GetCC returns the CC addresses
func (m *Message) GetCC() []Address {
	return m.cc
}

// GetBCC returns the BCC addresses
func (m *Message) GetBCC() []Address {
	return m.bcc
}

// GetReplyTo returns the reply-to addresses
func (m *Message) GetReplyTo() []Address {
	return m.replyTo
}

// GetSubject returns the subject
func (m *Message) GetSubject() string {
	return m.subject
}

// GetTextBody returns the plain text body
func (m *Message) GetTextBody() string {
	return m.textBody
}

// GetHTMLBody returns the HTML body
func (m *Message) GetHTMLBody() string {
	return m.htmlBody
}

// GetAttachments returns the attachments
func (m *Message) GetAttachments() []Attachment {
	return m.attachments
}

// GetHeaders returns the custom headers
func (m *Message) GetHeaders() map[string]string {
	return m.headers
}

// GetPriority returns the priority
func (m *Message) GetPriority() Priority {
	return m.priority
}
