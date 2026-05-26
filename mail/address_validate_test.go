package mail

import (
	"errors"
	"strings"
	"testing"
)

// TestNewAddressRejectsCRLFInEmail verifies that NewAddress refuses
// any email containing CR/LF/NUL/C0 control bytes. This is the
// happy-path constructor; callers using it on user-supplied input
// get the rejection up front rather than at the wire.
func TestNewAddressRejectsCRLFInEmail(t *testing.T) {
	cases := []struct {
		name  string
		email string
	}{
		{"LF in localpart", "victim\n@example.com"},
		{"CR in localpart", "victim\r@example.com"},
		{"CRLF after addr", "victim@example.com\r\nBcc: evil@x"},
		{"NUL in addr", "victim@example.com\x00"},
		{"vertical tab", "victim@example.com\x0b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewAddress(tc.email)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tc.email)
			}
		})
	}
}

// TestNewAddressRejectsCRLFInName verifies that NewAddress refuses
// any display name containing CR/LF/NUL/C0 control bytes.
func TestNewAddressRejectsCRLFInName(t *testing.T) {
	cases := []struct {
		name string
		dn   string
	}{
		{"LF", "Foo\nBcc: evil@x"},
		{"CR", "Foo\rBcc: evil@x"},
		{"CRLF", "Foo\r\nBcc: evil@x"},
		{"NUL", "Foo\x00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewAddress("ok@example.com", tc.dn)
			if err == nil {
				t.Fatalf("expected error for name %q, got nil", tc.dn)
			}
			if !errors.Is(err, ErrInvalidHeader) {
				t.Errorf("expected ErrInvalidHeader, got: %v", err)
			}
		})
	}
}

// TestNewAddressAcceptsCleanInput verifies that legitimate input
// produces a populated Address with no error.
func TestNewAddressAcceptsCleanInput(t *testing.T) {
	a, err := NewAddress("bob@example.com", "Bob")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Email != "bob@example.com" || a.Name != "Bob" {
		t.Errorf("Address fields not populated: %+v", a)
	}
}

// TestAddressValidateRejectsLiteralCRLF verifies that an Address
// literal carrying CR/LF in either Email or Name surfaces via the
// defence-in-depth Validate step. Drivers call Validate before
// serialisation; this test pins the contract Validate enforces.
func TestAddressValidateRejectsLiteralCRLF(t *testing.T) {
	cases := []struct {
		name string
		a    Address
	}{
		{
			name: "Email LF",
			a:    Address{Email: "victim@example.com\nBcc: evil@x"},
		},
		{
			name: "Email CR",
			a:    Address{Email: "victim@example.com\rBcc: evil@x"},
		},
		{
			name: "Email CRLF",
			a:    Address{Email: "victim@example.com\r\nBcc: evil@x"},
		},
		{
			name: "Email NUL",
			a:    Address{Email: "victim@example.com\x00"},
		},
		{
			name: "Name LF",
			a:    Address{Email: "ok@example.com", Name: "Foo\n"},
		},
		{
			name: "Name CR",
			a:    Address{Email: "ok@example.com", Name: "Foo\r"},
		},
		{
			name: "Name NUL",
			a:    Address{Email: "ok@example.com", Name: "Foo\x00"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.a.Validate()
			if err == nil {
				t.Fatalf("expected error from Validate, got nil for %+v", tc.a)
			}
			if !errors.Is(err, ErrInvalidHeader) {
				t.Errorf("expected ErrInvalidHeader, got: %v", err)
			}
			// Make sure the error message identifies which field tripped.
			if strings.Contains(tc.name, "Email") && !strings.Contains(err.Error(), "Email") {
				t.Errorf("expected error to mention Email field, got: %v", err)
			}
			if strings.Contains(tc.name, "Name") && !strings.Contains(err.Error(), "Name") {
				t.Errorf("expected error to mention Name field, got: %v", err)
			}
		})
	}
}

// TestAddressValidateRejectsRecipientListSmuggling pins the
// driver-boundary single-mailbox invariant: a literal-built Address
// whose Email field carries a recipient list, an embedded display
// name, or a Name field with address-grammar specials must be
// rejected before serialisation. Pre-fix the Validate function only
// covered CR/LF control bytes, so payloads like
//
//	mail.Address{Email: "victim@example.com, attacker@evil.com"}
//
// reached Mailgun / Postmark as a two-recipient list because the
// REST bodies and SMTP To header are list-aware. Validate now mirrors
// validateAddressField on the setter path.
func TestAddressValidateRejectsRecipientListSmuggling(t *testing.T) {
	cases := []struct {
		name string
		addr Address
	}{
		{"comma-separated recipient list",
			Address{Email: "victim@example.com, attacker@evil.com"}},
		{"semicolon-separated recipient list",
			Address{Email: "victim@example.com; attacker@evil.com"}},
		{"embedded display name in email",
			Address{Email: "Bob <bob@example.com>"}},
		{"angle-bracketed bare addr-spec",
			Address{Email: "<bob@example.com>"}},
		{"unparseable email",
			Address{Email: "not-an-email"}},
		{"display-name impersonation special",
			Address{Email: "ok@example.com", Name: `Bob" <evil@x>, "Real`}},
		{"display-name with comma list-separator",
			Address{Email: "ok@example.com", Name: "Bob, Alice"}},
		{"display-name with angle bracket",
			Address{Email: "ok@example.com", Name: "Bob <evil>"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.addr.Validate(); err == nil {
				t.Fatalf("Validate must reject %+v", tc.addr)
			}
		})
	}
}

// TestAddressValidateAcceptsCleanLiteral verifies that an Address
// literal with clean fields passes Validate. The check must be a
// no-op on the common path.
func TestAddressValidateAcceptsCleanLiteral(t *testing.T) {
	cases := []Address{
		{Email: "ok@example.com"},
		{Email: "ok@example.com", Name: "Alice"},
		{Email: "ok+tag@sub.example.com", Name: "Alice O'Brien"},
		{Email: "ok@example.com", Name: "Unicode ネコ"},
		{Email: "", Name: ""}, // zero value is fine here; emptiness is policed elsewhere.
	}
	for i, a := range cases {
		if err := a.Validate(); err != nil {
			t.Errorf("case %d (%+v): unexpected error %v", i, a, err)
		}
	}
}
