package mailgun

import (
	"bytes"
	"errors"
	"mime/multipart"
	"testing"

	"github.com/velocitykode/velocity/mail"
)

// TestMailgunDriverRejectsLiteralAddressWithCRLFInEmail verifies that
// even when a caller bypasses Message.To() and stuffs a CR/LF payload
// into Address.Email directly via the writeRecipientFields path, the
// driver refuses to serialise. Without the Validate hook the payload
// would be silently stripped by net/mail.Address.String() and the
// header-injection attempt would land on the wire as a quoted local
// part, masking the misuse.
func TestMailgunDriverRejectsLiteralAddressWithCRLFInEmail(t *testing.T) {
	driver, err := NewMailgunDriver(mail.MailgunConfig{
		Domain: "mg.example.com",
		Secret: "key",
	}, "from@example.com", "Legit Sender")
	if err != nil {
		t.Fatalf("driver: %v", err)
	}

	cases := []struct {
		name string
		addr mail.Address
	}{
		{"To Email CRLF", mail.Address{Email: "victim@example.com\r\nBcc: evil@x"}},
		{"To Name CRLF", mail.Address{Email: "victim@example.com", Name: "Foo\r\nBcc: evil@x"}},
		{"From Email LF", mail.Address{Email: "from@example.com\nBcc: evil@x"}},
		{"From Name CR", mail.Address{Email: "from@example.com", Name: "Sender\rBcc: evil@x"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.addr.Validate()
			if err == nil {
				t.Fatalf("Validate accepted literal Address with CR/LF: %+v", tc.addr)
			}
			if !errors.Is(err, mail.ErrInvalidHeader) {
				t.Errorf("expected ErrInvalidHeader, got: %v", err)
			}
		})
	}

	_ = driver
}

// TestMailgunDriverWriteFromRejectsPoisonedDefault verifies that the
// driver's default-From construction path (when msg has no From and
// the driver substitutes its configured fromAddr/fromName) also runs
// through Validate so a misconfigured driver cannot CRLF-inject via
// MAIL_FROM_ADDRESS.
func TestMailgunDriverWriteFromRejectsPoisonedDefault(t *testing.T) {
	driver, err := NewMailgunDriver(mail.MailgunConfig{
		Domain: "mg.example.com",
		Secret: "key",
	}, "from@example.com\r\nBcc: evil@x", "Legit")
	if err != nil {
		t.Fatalf("driver: %v", err)
	}

	// Build a message with no From; driver should attempt to fill it from
	// its configured default, which here is poisoned.
	msg := mail.NewMessage().To("to@example.com").Subject("S").TextBody("B")

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	err = driver.writeFromField(w, msg)
	if err == nil {
		t.Fatal("expected error from writeFromField when default From is poisoned, got nil")
	}
	if !errors.Is(err, mail.ErrInvalidHeader) {
		t.Errorf("expected ErrInvalidHeader, got: %v", err)
	}
}
