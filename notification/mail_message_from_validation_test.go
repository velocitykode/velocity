package notification

import (
	"strings"
	"testing"
)

// TestMailMessageFromRejectsCRLFInEmail verifies that
// MailMessage.From now routes through mail.NewAddress, so a CR/LF
// injection payload in the email argument is silently dropped (no
// poisoned From planted on the MailMessage) rather than stored as a
// raw literal that drivers would later try to serialise.
//
// This pins the M-20 fix: the previous implementation built
// mail.Address{Email: email} directly, bypassing the validation
// surface entirely.
func TestMailMessageFromRejectsCRLFInEmail(t *testing.T) {
	cases := []struct {
		name  string
		email string
	}{
		{"LF", "victim@example.com\nBcc: evil@x"},
		{"CR", "victim@example.com\rBcc: evil@x"},
		{"CRLF", "victim@example.com\r\nBcc: evil@x"},
		{"NUL", "victim@example.com\x00"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMailMessage().From(tc.email, "Sender")
			from := m.GetFrom()
			if strings.ContainsAny(from.Email, "\r\n\x00") {
				t.Errorf("From.Email leaked CR/LF/NUL: %q", from.Email)
			}
			// On rejection, the From should be zero (the bad input does
			// not overwrite m.from). Verify the zero state.
			if from.Email != "" || from.Name != "" {
				t.Errorf("expected From to remain zero on invalid input, got %+v", from)
			}
		})
	}
}

// TestMailMessageFromRejectsCRLFInName verifies that CR/LF in the
// display name is also rejected via the NewAddress validation step.
func TestMailMessageFromRejectsCRLFInName(t *testing.T) {
	m := NewMailMessage().From("ok@example.com", "Foo\r\nBcc: evil@x")
	from := m.GetFrom()
	if strings.ContainsAny(from.Name, "\r\n") {
		t.Errorf("From.Name leaked CR/LF: %q", from.Name)
	}
	if from.Email != "" || from.Name != "" {
		t.Errorf("expected From to remain zero on invalid input, got %+v", from)
	}
}

// TestMailMessageFromAcceptsCleanInput verifies that legitimate
// addresses still land on the MailMessage as expected.
func TestMailMessageFromAcceptsCleanInput(t *testing.T) {
	m := NewMailMessage().From("bob@example.com", "Bob")
	from := m.GetFrom()
	if from.Email != "bob@example.com" {
		t.Errorf("expected Email bob@example.com, got %q", from.Email)
	}
	if from.Name != "Bob" {
		t.Errorf("expected Name Bob, got %q", from.Name)
	}
}
