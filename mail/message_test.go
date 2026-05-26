package mail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewMessage(t *testing.T) {
	msg := NewMessage()

	if msg == nil {
		t.Fatal("Expected message to be created")
	}

	if msg.priority != NormalPriority {
		t.Error("Expected default priority to be NormalPriority")
	}

	if msg.to == nil || msg.cc == nil || msg.bcc == nil {
		t.Error("Expected slices to be initialized")
	}
}

func TestMessageFrom(t *testing.T) {
	msg := NewMessage().From("sender@example.com")

	if msg.GetFrom().Email != "sender@example.com" {
		t.Errorf("Expected sender@example.com, got %s", msg.GetFrom().Email)
	}

	msg = NewMessage().From("sender@example.com", "Sender Name")
	if msg.GetFrom().Name != "Sender Name" {
		t.Errorf("Expected 'Sender Name', got '%s'", msg.GetFrom().Name)
	}
}

func TestMessageTo(t *testing.T) {
	msg := NewMessage().
		To("user1@example.com").
		To("user2@example.com", "User Two")

	to := msg.GetTo()
	if len(to) != 2 {
		t.Errorf("Expected 2 recipients, got %d", len(to))
	}

	if to[0].Email != "user1@example.com" {
		t.Errorf("Expected user1@example.com, got %s", to[0].Email)
	}

	if to[1].Name != "User Two" {
		t.Errorf("Expected 'User Two', got '%s'", to[1].Name)
	}
}

func TestMessageCC(t *testing.T) {
	msg := NewMessage().
		CC("cc1@example.com").
		CC("cc2@example.com", "CC Two")

	cc := msg.GetCC()
	if len(cc) != 2 {
		t.Errorf("Expected 2 CC recipients, got %d", len(cc))
	}

	if cc[0].Email != "cc1@example.com" {
		t.Errorf("Expected cc1@example.com, got %s", cc[0].Email)
	}
}

func TestMessageBCC(t *testing.T) {
	msg := NewMessage().
		BCC("bcc1@example.com").
		BCC("bcc2@example.com", "BCC Two")

	bcc := msg.GetBCC()
	if len(bcc) != 2 {
		t.Errorf("Expected 2 BCC recipients, got %d", len(bcc))
	}

	if bcc[0].Email != "bcc1@example.com" {
		t.Errorf("Expected bcc1@example.com, got %s", bcc[0].Email)
	}
}

func TestMessageReplyTo(t *testing.T) {
	msg := NewMessage().
		ReplyTo("reply@example.com").
		ReplyTo("reply2@example.com", "Reply Two")

	replyTo := msg.GetReplyTo()
	if len(replyTo) != 2 {
		t.Errorf("Expected 2 reply-to addresses, got %d", len(replyTo))
	}

	if replyTo[0].Email != "reply@example.com" {
		t.Errorf("Expected reply@example.com, got %s", replyTo[0].Email)
	}
}

func TestMessageSubject(t *testing.T) {
	msg := NewMessage().Subject("Test Subject")

	if msg.GetSubject() != "Test Subject" {
		t.Errorf("Expected 'Test Subject', got '%s'", msg.GetSubject())
	}
}

func TestMessageBody(t *testing.T) {
	msg := NewMessage().Body("Plain text body")

	if msg.GetTextBody() != "Plain text body" {
		t.Errorf("Expected 'Plain text body', got '%s'", msg.GetTextBody())
	}
}

func TestMessageTextBody(t *testing.T) {
	msg := NewMessage().TextBody("Text body")

	if msg.GetTextBody() != "Text body" {
		t.Errorf("Expected 'Text body', got '%s'", msg.GetTextBody())
	}
}

func TestMessageHTMLBody(t *testing.T) {
	msg := NewMessage().HTMLBody("<h1>HTML body</h1>")

	if msg.GetHTMLBody() != "<h1>HTML body</h1>" {
		t.Errorf("Expected '<h1>HTML body</h1>', got '%s'", msg.GetHTMLBody())
	}
}

func TestMessageAttachData(t *testing.T) {
	data := []byte("test data")
	msg := NewMessage().AttachData(data, "test.txt", "text/plain")

	attachments := msg.GetAttachments()
	if len(attachments) != 1 {
		t.Errorf("Expected 1 attachment, got %d", len(attachments))
	}

	if attachments[0].Name != "test.txt" {
		t.Errorf("Expected 'test.txt', got '%s'", attachments[0].Name)
	}

	if attachments[0].ContentType != "text/plain" {
		t.Errorf("Expected 'text/plain', got '%s'", attachments[0].ContentType)
	}

	if string(attachments[0].Data) != "test data" {
		t.Errorf("Expected 'test data', got '%s'", string(attachments[0].Data))
	}
}

// attachRoot creates a temp directory, writes a file with the given name
// and content into it, opens an *os.Root for the directory, and returns
// both. The Root is closed via t.Cleanup. The Message tests use this to
// satisfy the AttachFile root-registration requirement.
func attachRoot(t *testing.T, name, content string) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	if name != "" {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { root.Close() })
	return root, dir
}

func TestMessageAttachFile(t *testing.T) {
	root, _ := attachRoot(t, "test.txt", "test file content")

	msg, err := NewMessage().WithAttachmentRoot(root).AttachFile("test.txt")
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	attachments := msg.GetAttachments()
	if len(attachments) != 1 {
		t.Errorf("Expected 1 attachment, got %d", len(attachments))
	}

	if string(attachments[0].Data) != "test file content" {
		t.Errorf("Expected 'test file content', got %q", string(attachments[0].Data))
	}
}

func TestMessageAttachFileReturnsErrorOnMissing(t *testing.T) {
	root, _ := attachRoot(t, "", "")
	_, err := NewMessage().WithAttachmentRoot(root).AttachFile("nonexistent.txt")
	if err == nil {
		t.Error("Expected error when attaching non-existent file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected wrapped os.ErrNotExist, got %v", err)
	}
}

func TestMessageAttachFileRejectsTraversal(t *testing.T) {
	root, _ := attachRoot(t, "", "")
	_, err := NewMessage().WithAttachmentRoot(root).AttachFile("../../etc/passwd")
	if err == nil {
		t.Fatal("Expected error for path traversal")
	}
	if !errors.Is(err, ErrAttachmentPathOutsideRoot) {
		t.Errorf("expected ErrAttachmentPathOutsideRoot, got %v", err)
	}
}

func TestMessageHeader(t *testing.T) {
	msg := NewMessage().
		Header("X-Custom-Header", "custom-value").
		Header("X-Another-Header", "another-value")

	headers := msg.GetHeaders()
	if len(headers) != 2 {
		t.Errorf("Expected 2 headers, got %d", len(headers))
	}

	if headers["X-Custom-Header"] != "custom-value" {
		t.Errorf("Expected 'custom-value', got '%s'", headers["X-Custom-Header"])
	}
}

func TestMessagePriority(t *testing.T) {
	msg := NewMessage().Priority(HighPriority)

	if msg.GetPriority() != HighPriority {
		t.Errorf("Expected HighPriority, got %d", msg.GetPriority())
	}
}

func TestMessageFluentAPI(t *testing.T) {
	msg := NewMessage().
		From("sender@example.com", "Sender").
		To("recipient@example.com", "Recipient").
		CC("cc@example.com").
		BCC("bcc@example.com").
		ReplyTo("reply@example.com").
		Subject("Fluent API Test").
		TextBody("Plain text").
		HTMLBody("<p>HTML text</p>").
		Header("X-Custom", "value").
		Priority(HighPriority).
		AttachData([]byte("data"), "file.txt", "text/plain")

	// Verify all fields were set
	if msg.GetFrom().Email != "sender@example.com" {
		t.Error("From not set")
	}
	if len(msg.GetTo()) != 1 {
		t.Error("To not set")
	}
	if len(msg.GetCC()) != 1 {
		t.Error("CC not set")
	}
	if len(msg.GetBCC()) != 1 {
		t.Error("BCC not set")
	}
	if len(msg.GetReplyTo()) != 1 {
		t.Error("ReplyTo not set")
	}
	if msg.GetSubject() != "Fluent API Test" {
		t.Error("Subject not set")
	}
	if msg.GetTextBody() != "Plain text" {
		t.Error("TextBody not set")
	}
	if msg.GetHTMLBody() != "<p>HTML text</p>" {
		t.Error("HTMLBody not set")
	}
	if len(msg.GetHeaders()) != 1 {
		t.Error("Headers not set")
	}
	if msg.GetPriority() != HighPriority {
		t.Error("Priority not set")
	}
	if len(msg.GetAttachments()) != 1 {
		t.Error("Attachments not set")
	}
}

func TestMessageTemplate(t *testing.T) {
	// Create temp template directory
	tmpDir, err := os.MkdirTemp("", "mail-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	templatesDir := tmpDir + "/emails"
	os.MkdirAll(templatesDir, 0755)

	templateContent := `<h1>Hello {{.Name}}!</h1>`
	templateFile := templatesDir + "/test.html"
	if err := os.WriteFile(templateFile, []byte(templateContent), 0644); err != nil {
		t.Fatalf("Failed to write template: %v", err)
	}

	// Set template path to our temp directory
	SetTemplatePath(templatesDir)
	defer SetTemplatePath("resources/views/emails") // Restore default

	data := struct{ Name string }{Name: "World"}
	msg, err := NewMessage().Template("test", data)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	expected := "<h1>Hello World!</h1>"
	if msg.GetHTMLBody() != expected {
		t.Errorf("Expected '%s', got '%s'", expected, msg.GetHTMLBody())
	}
}

func TestMessageTemplateReturnsErrorOnMissing(t *testing.T) {
	_, err := NewMessage().Template("nonexistent", nil)
	if err == nil {
		t.Error("Expected error when template file not found")
	}
}

func TestMessageTemplateRejectsTraversal(t *testing.T) {
	_, err := NewMessage().Template("../../etc/passwd", nil)
	if err == nil {
		t.Error("Expected error for path traversal in template name")
	}
}

// --- Attachment size regression tests ----------------------------------------

// largeFile creates a sparse file of exactly size bytes inside dir. It uses
// a seek-then-single-byte trick to keep the test fast even at 25 MiB.
// Returns the base name (relative to dir).
func largeFile(t *testing.T, dir string, size int64) string {
	t.Helper()
	f, err := os.CreateTemp(dir, "mail-attach-*.bin")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()
	if size > 0 {
		if _, err := f.Seek(size-1, 0); err != nil {
			t.Fatalf("Seek: %v", err)
		}
		if _, err := f.Write([]byte{0}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	return filepath.Base(f.Name())
}

func TestAttachFile_RejectsOversized(t *testing.T) {
	root, dir := attachRoot(t, "", "")
	name := largeFile(t, dir, DefaultMaxAttachmentSize+1)
	_, err := NewMessage().WithAttachmentRoot(root).AttachFile(name)
	if err == nil {
		t.Fatal("expected ErrAttachmentTooLarge, got nil")
	}
	if !errors.Is(err, ErrAttachmentTooLarge) {
		t.Errorf("expected ErrAttachmentTooLarge, got %v", err)
	}
}

func TestAttachFile_AcceptsAtLimit(t *testing.T) {
	root, dir := attachRoot(t, "", "")
	name := largeFile(t, dir, DefaultMaxAttachmentSize)
	msg, err := NewMessage().WithAttachmentRoot(root).AttachFile(name)
	if err != nil {
		t.Fatalf("expected no error at exact limit, got %v", err)
	}
	if got := len(msg.GetAttachments()); got != 1 {
		t.Errorf("expected 1 attachment, got %d", got)
	}
}

func TestAttachData_RejectsOversized(t *testing.T) {
	data := make([]byte, DefaultMaxAttachmentSize+1)
	msg := NewMessage().AttachData(data, "big.bin", "application/octet-stream")
	if err := msg.Err(); err == nil {
		t.Fatal("expected deferred ErrAttachmentTooLarge on message, got nil")
	} else if !errors.Is(err, ErrAttachmentTooLarge) {
		t.Errorf("expected ErrAttachmentTooLarge, got %v", err)
	}
	if len(msg.GetAttachments()) != 0 {
		t.Errorf("oversized attachment must not be appended, got %d", len(msg.GetAttachments()))
	}
}

func TestAttachData_AcceptsAtLimit(t *testing.T) {
	data := make([]byte, DefaultMaxAttachmentSize)
	msg := NewMessage().AttachData(data, "big.bin", "application/octet-stream")
	if err := msg.Err(); err != nil {
		t.Fatalf("expected no error at exact limit, got %v", err)
	}
	if len(msg.GetAttachments()) != 1 {
		t.Errorf("expected 1 attachment, got %d", len(msg.GetAttachments()))
	}
}

func TestMaxAttachmentSize_ConfigDefault(t *testing.T) {
	// Zero-value MailConfig must yield DefaultMaxAttachmentSize, NOT "unlimited".
	prev := GetDefaultMaxAttachmentSize()
	t.Cleanup(func() { SetDefaultMaxAttachmentSize(prev) })

	SetDefaultMaxAttachmentSize(0) // simulate zero-value config
	if got := GetDefaultMaxAttachmentSize(); got != DefaultMaxAttachmentSize {
		t.Errorf("zero value should resolve to DefaultMaxAttachmentSize=%d, got %d",
			DefaultMaxAttachmentSize, got)
	}
	if got := NewMessage().MaxAttachmentSize(); got != DefaultMaxAttachmentSize {
		t.Errorf("NewMessage().MaxAttachmentSize() = %d, want %d",
			got, DefaultMaxAttachmentSize)
	}
}

func TestWithMaxAttachmentSize_OverridesDefault(t *testing.T) {
	msg := NewMessage().WithMaxAttachmentSize(1024)
	if got := msg.MaxAttachmentSize(); got != 1024 {
		t.Errorf("expected 1024, got %d", got)
	}
	// Zero or negative resets to default.
	msg.WithMaxAttachmentSize(0)
	if got := msg.MaxAttachmentSize(); got != DefaultMaxAttachmentSize {
		t.Errorf("expected DefaultMaxAttachmentSize after 0, got %d", got)
	}
}

func TestAttachFile_HonoursPerMessageLimit(t *testing.T) {
	// Create a 2 KiB file, cap the message at 1 KiB.
	root, dir := attachRoot(t, "", "")
	name := largeFile(t, dir, 2048)
	_, err := NewMessage().WithAttachmentRoot(root).WithMaxAttachmentSize(1024).AttachFile(name)
	if !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("expected ErrAttachmentTooLarge, got %v", err)
	}
}

// --- Header / Subject / Address CRLF regression tests -----------------------

func TestHeader_RejectsCRLFInValue(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"crlf_plus_bcc", "foo\r\nBcc: attacker@evil.com"},
		{"lf_plus_header", "foo\nX-Injected: true"},
		{"cr_only", "foo\rBcc: attacker@evil.com"},
		{"nul_byte", "foo\x00bar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := NewMessage().Header("X-Test", tc.value)
			if !errors.Is(msg.Err(), ErrInvalidHeader) {
				t.Fatalf("expected ErrInvalidHeader, got %v", msg.Err())
			}
			if _, ok := msg.GetHeaders()["X-Test"]; ok {
				t.Error("header must not be stored when validation fails")
			}
		})
	}
}

func TestHeader_RejectsCRLFInKey(t *testing.T) {
	cases := []string{
		"X-Foo\r\n",
		"X-Foo\n",
		"X-Foo\r",
		"X-Foo: bar", // colon terminates the field name
		"X Foo",      // space inside a token is illegal
		"",           // empty name
	}
	for _, k := range cases {
		t.Run(k, func(t *testing.T) {
			msg := NewMessage().Header(k, "value")
			if !errors.Is(msg.Err(), ErrInvalidHeader) {
				t.Errorf("key %q: expected ErrInvalidHeader, got %v", k, msg.Err())
			}
		})
	}
}

func TestHeader_RejectsControlChars(t *testing.T) {
	// NUL in value
	m1 := NewMessage().Header("X-Test", "foo\x00bar")
	if !errors.Is(m1.Err(), ErrInvalidHeader) {
		t.Errorf("NUL in value: expected ErrInvalidHeader, got %v", m1.Err())
	}
	// DEL in value
	m2 := NewMessage().Header("X-Test", "foo\x7fbar")
	if !errors.Is(m2.Err(), ErrInvalidHeader) {
		t.Errorf("DEL in value: expected ErrInvalidHeader, got %v", m2.Err())
	}
	// ESC in value
	m3 := NewMessage().Header("X-Test", "foo\x1bbar")
	if !errors.Is(m3.Err(), ErrInvalidHeader) {
		t.Errorf("ESC in value: expected ErrInvalidHeader, got %v", m3.Err())
	}
}

func TestSubject_RejectsCRLF(t *testing.T) {
	msg := NewMessage().Subject("Hello\r\nBcc: attacker@evil.com")
	if !errors.Is(msg.Err(), ErrInvalidHeader) {
		t.Fatalf("expected ErrInvalidHeader, got %v", msg.Err())
	}
	if msg.GetSubject() != "" {
		t.Errorf("bad subject must not be stored, got %q", msg.GetSubject())
	}
}

func TestTo_RejectsCRLF(t *testing.T) {
	// CRLF in email
	msg1 := NewMessage().To("victim@example.com\r\nBcc: evil@evil.com")
	if !errors.Is(msg1.Err(), ErrInvalidHeader) {
		t.Errorf("CRLF in email: expected ErrInvalidHeader, got %v", msg1.Err())
	}
	if len(msg1.GetTo()) != 0 {
		t.Error("oversize rejected To must not be appended")
	}
	// CRLF in display name
	msg2 := NewMessage().To("victim@example.com", "Bob\r\nBcc: evil@evil.com")
	if !errors.Is(msg2.Err(), ErrInvalidHeader) {
		t.Errorf("CRLF in name: expected ErrInvalidHeader, got %v", msg2.Err())
	}
}

// --- Address grammar-special rejection (H-18) -------------------------------

func TestTo_RejectsAddressGrammarSpecialsInName(t *testing.T) {
	cases := []struct {
		name    string
		display string
	}{
		{"double_quote", `Bob" <evil@x.com>, "Real`},
		{"angle_open", "Bob <evil"},
		{"angle_close", "Bob>"},
		{"comma", "Bob, the Builder"},
		{"semicolon", "Bob; group"},
		{"colon", "group: Bob"},
		{"backslash", `Bob\ Builder`},
		{"paren_open", "Bob (the"},
		{"paren_close", "Bob) Builder"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := NewMessage().To("victim@example.com", tc.display)
			if !errors.Is(msg.Err(), ErrInvalidHeader) {
				t.Fatalf("display name %q must be rejected, got err=%v", tc.display, msg.Err())
			}
			if len(msg.GetTo()) != 0 {
				t.Errorf("rejected recipient must not be appended, got %+v", msg.GetTo())
			}
		})
	}
}

func TestFrom_RejectsInjectionPayloadInName(t *testing.T) {
	// The canonical recipient-impersonation payload from the audit:
	// without quoting, an MTA could split this into two mailboxes.
	msg := NewMessage().From("victim@example.com", `Bob" <attacker@evil.com>, "Real Bob`)
	if !errors.Is(msg.Err(), ErrInvalidHeader) {
		t.Fatalf("injection payload must be rejected, got %v", msg.Err())
	}
	if msg.GetFrom().Email != "" {
		t.Errorf("rejected From must not be stored, got %+v", msg.GetFrom())
	}
}

func TestAddress_LegitimateUnicodeNameRoundTrips(t *testing.T) {
	// Unicode names without ASCII grammar specials must pass the validator
	// and serialise via net/mail.Address (RFC 2047 encoded-word).
	msg := NewMessage().To("muller@example.com", "Müller")
	if err := msg.Err(); err != nil {
		t.Fatalf("Unicode display name must be accepted, got %v", err)
	}
	addrs := msg.GetTo()
	if len(addrs) != 1 || addrs[0].Name != "Müller" {
		t.Fatalf("expected one recipient with Name=Müller, got %+v", addrs)
	}
	// Address.String() must produce a header-safe form: the raw name
	// is unsafe under SMTP gateways that are not 8-bit clean, so
	// net/mail encodes it as a quoted-printable encoded-word.
	got := addrs[0].String()
	if !strings.Contains(got, "<muller@example.com>") {
		t.Errorf("serialised form must wrap addr-spec in angle brackets, got %q", got)
	}
}

func TestAddress_String_QuotesNameWithSpace(t *testing.T) {
	// Even a benign space in the display name forces RFC 5322 phrase
	// quoting; the result must round-trip unambiguously, not be left
	// as bare "Test User <addr>".
	addr := Address{Email: "test@example.com", Name: "Test User"}
	got := addr.String()
	if got != `"Test User" <test@example.com>` {
		t.Errorf("expected RFC 5322 quoted phrase, got %q", got)
	}
}

func TestFromCCBccReplyTo_RejectsCRLF(t *testing.T) {
	t.Run("From_email", func(t *testing.T) {
		msg := NewMessage().From("sender@example.com\r\nBcc: evil@x.com")
		if !errors.Is(msg.Err(), ErrInvalidHeader) {
			t.Fatalf("got %v", msg.Err())
		}
		if msg.GetFrom().Email != "" {
			t.Error("rejected From must not be stored")
		}
	})
	t.Run("From_name", func(t *testing.T) {
		msg := NewMessage().From("sender@example.com", "Evil\r\nX-Injected: 1")
		if !errors.Is(msg.Err(), ErrInvalidHeader) {
			t.Fatalf("got %v", msg.Err())
		}
	})
	t.Run("CC", func(t *testing.T) {
		msg := NewMessage().CC("cc@example.com\nBcc: evil@x.com")
		if !errors.Is(msg.Err(), ErrInvalidHeader) {
			t.Fatalf("got %v", msg.Err())
		}
	})
	t.Run("BCC", func(t *testing.T) {
		msg := NewMessage().BCC("bcc@example.com\r\nBcc: evil@x.com")
		if !errors.Is(msg.Err(), ErrInvalidHeader) {
			t.Fatalf("got %v", msg.Err())
		}
	})
	t.Run("ReplyTo", func(t *testing.T) {
		msg := NewMessage().ReplyTo("r@example.com\r\nBcc: evil@x.com")
		if !errors.Is(msg.Err(), ErrInvalidHeader) {
			t.Fatalf("got %v", msg.Err())
		}
	})
}

// --- Error-surfacing tests --------------------------------------------------

func TestManagerSend_RejectsMessageWithSetterError(t *testing.T) {
	mgr := NewManager()
	mock := &mockMailer{sent: make([]*Message, 0)}
	mgr.SetChannel("default", mock)

	msg := NewMessage().
		To("user@example.com").
		Subject("hi\r\nBcc: attacker@evil.com"). // bad: accumulates err
		Body("body")

	err := mgr.Send(context.Background(), "default", msg)
	if !errors.Is(err, ErrInvalidHeader) {
		t.Fatalf("Manager.Send must surface msg.Err(); got %v", err)
	}
	if mock.SentCount() != 0 {
		t.Errorf("driver must not be called for a bad message, got %d sends", mock.SentCount())
	}
}

func TestCheckedMailer_RejectsMessageWithSetterError(t *testing.T) {
	mock := &mockMailer{sent: make([]*Message, 0)}
	cm := &checkedMailer{inner: mock}

	oversized := make([]byte, DefaultMaxAttachmentSize+1)
	msg := NewMessage().AttachData(oversized, "big.bin", "application/octet-stream")

	err := cm.Send(context.Background(), msg)
	if !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("expected ErrAttachmentTooLarge from checkedMailer.Send, got %v", err)
	}
	if mock.SentCount() != 0 {
		t.Error("inner mailer must not be invoked on deferred error")
	}
}

// --- Path coverage: AttachFile happy path with per-message limit ------------

func TestAttachFile_ReadsViaLimitReader(t *testing.T) {
	// 1 KiB file, 1 MiB limit; succeeds cleanly.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.bin"), make([]byte, 1024), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { root.Close() })
	msg, err := NewMessage().WithAttachmentRoot(root).WithMaxAttachmentSize(1 << 20).AttachFile("ok.bin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(msg.GetAttachments()[0].Data); got != 1024 {
		t.Errorf("expected 1024 bytes, got %d", got)
	}
}

// --- Attachment root containment (H-19) -------------------------------------

func TestAttachFile_NoRootConfigured_Rejected(t *testing.T) {
	// Defensive: clear the package default for the duration of the test
	// so a stray earlier call to SetDefaultAttachmentRoot cannot mask
	// the failure mode this test is asserting.
	prev := GetDefaultAttachmentRoot()
	SetDefaultAttachmentRoot(nil)
	t.Cleanup(func() { SetDefaultAttachmentRoot(prev) })

	_, err := NewMessage().AttachFile("/etc/passwd")
	if !errors.Is(err, ErrAttachmentRootRequired) {
		t.Fatalf("expected ErrAttachmentRootRequired, got %v", err)
	}
	// Relative path with no root configured: same answer. Root is
	// mandatory regardless of path shape, so callers cannot accidentally
	// fall back to process-cwd file reads.
	_, err = NewMessage().AttachFile("relative.txt")
	if !errors.Is(err, ErrAttachmentRootRequired) {
		t.Errorf("expected ErrAttachmentRootRequired for relative path too, got %v", err)
	}
}

func TestAttachFile_AbsolutePathRejected(t *testing.T) {
	root, _ := attachRoot(t, "ok.txt", "data")
	_, err := NewMessage().WithAttachmentRoot(root).AttachFile("/etc/passwd")
	if err == nil {
		t.Fatal("absolute path must be rejected even with a configured root")
	}
	if !errors.Is(err, ErrAttachmentPathOutsideRoot) {
		t.Errorf("expected ErrAttachmentPathOutsideRoot, got %v", err)
	}
}

func TestAttachFile_TraversalEvenInRootRejected(t *testing.T) {
	// Create two sibling dirs: secrets/ (out of root) and attachments/
	// (the configured root). A ".." segment from inside attachments/
	// must NOT be able to climb up to secrets/.
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "secrets"), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "secrets", "creds.txt"), []byte("PASSWORD"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	attDir := filepath.Join(tmp, "attachments")
	if err := os.Mkdir(attDir, 0o700); err != nil {
		t.Fatalf("Mkdir attachments: %v", err)
	}
	root, err := os.OpenRoot(attDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { root.Close() })

	_, err = NewMessage().WithAttachmentRoot(root).AttachFile("../secrets/creds.txt")
	if err == nil {
		t.Fatal("`..` traversal must be rejected by os.Root")
	}
	if !errors.Is(err, ErrAttachmentPathOutsideRoot) {
		t.Errorf("expected ErrAttachmentPathOutsideRoot, got %v", err)
	}
}

func TestAttachFile_SymlinkEscapeRejected(t *testing.T) {
	// Place a real file at $TMP/secrets/passwd, then put a symlink at
	// $TMP/attachments/escape that points to it. (*os.Root).Open must
	// refuse to follow the symlink out of the root.
	tmp := t.TempDir()
	secrets := filepath.Join(tmp, "secrets")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatalf("Mkdir secrets: %v", err)
	}
	target := filepath.Join(secrets, "passwd")
	if err := os.WriteFile(target, []byte("ROOT-SECRET"), 0o600); err != nil {
		t.Fatalf("WriteFile target: %v", err)
	}
	attDir := filepath.Join(tmp, "attachments")
	if err := os.Mkdir(attDir, 0o700); err != nil {
		t.Fatalf("Mkdir attachments: %v", err)
	}
	link := filepath.Join(attDir, "escape")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}
	root, err := os.OpenRoot(attDir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { root.Close() })

	_, err = NewMessage().WithAttachmentRoot(root).AttachFile("escape")
	if err == nil {
		t.Fatal("symlink escape must be rejected")
	}
	if !errors.Is(err, ErrAttachmentPathOutsideRoot) {
		t.Errorf("expected ErrAttachmentPathOutsideRoot, got %v", err)
	}
}

func TestAttachFile_DefaultRootFallback(t *testing.T) {
	// When the message has no per-message root but the package default
	// is set, AttachFile must pick it up.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { root.Close() })

	prev := GetDefaultAttachmentRoot()
	SetDefaultAttachmentRoot(root)
	t.Cleanup(func() { SetDefaultAttachmentRoot(prev) })

	msg, err := NewMessage().AttachFile("ok.txt")
	if err != nil {
		t.Fatalf("expected fallback to default root, got %v", err)
	}
	if string(msg.GetAttachments()[0].Data) != "hi" {
		t.Errorf("unexpected attachment content: %q", msg.GetAttachments()[0].Data)
	}
}

func TestManager_SetAttachmentRoot_DelegatesToPackageDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "viaMgr.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { root.Close() })

	prev := GetDefaultAttachmentRoot()
	t.Cleanup(func() { SetDefaultAttachmentRoot(prev) })

	mgr := NewManager()
	mgr.SetAttachmentRoot(root)
	if got := GetDefaultAttachmentRoot(); got != root {
		t.Fatalf("Manager.SetAttachmentRoot did not propagate to package default: got %p want %p", got, root)
	}
	msg, err := NewMessage().AttachFile("viaMgr.txt")
	if err != nil {
		t.Fatalf("AttachFile after Manager.SetAttachmentRoot: %v", err)
	}
	if string(msg.GetAttachments()[0].Data) != "hello" {
		t.Errorf("unexpected attachment content: %q", msg.GetAttachments()[0].Data)
	}
}
