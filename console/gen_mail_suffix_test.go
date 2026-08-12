package console

import (
	"testing"
)

// TestToMailName covers the mailable generator's suffix stripping in both case
// forms. The lowercase "mail" form used to be absent entirely, so `vel gen mail
// Mail` was rejected as suffix-only while `vel gen mail mail` normalised to
// "Mail" and generated internal/mail/mail.go.
//
// It cannot be a plain TrimSuffix like the rest of the family: "mail" is a
// common ending of a real mailable name, so the lowercase form is only stripped
// at a word boundary.
func TestToMailName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// Suffix-only names normalise to empty in every case form, which is
		// what requireNormalizedName rejects.
		{"bare Mailable", "Mailable", ""},
		{"bare mailable", "mailable", ""},
		{"bare Mail", "Mail", ""},
		{"bare mail", "mail", ""},

		// Redundant suffix on a real name is stripped.
		{"pascal Mail suffix", "OrderConfirmationMail", "OrderConfirmation"},
		{"pascal Mailable suffix", "InvoiceMailable", "Invoice"},
		{"snake mail suffix", "welcome_mail", "Welcome"},
		{"snake mailable suffix", "welcome_mailable", "Welcome"},
		{"kebab mail suffix", "welcome-mail", "Welcome"},

		// "mail" inside a word is part of the name, not a suffix.
		{"email is not a suffix", "WelcomeEmail", "WelcomeEmail"},
		{"snake email is not a suffix", "welcome_email", "WelcomeEmail"},
		{"gmail is not a suffix", "Gmail", "Gmail"},
		{"sendmail is not a suffix", "sendmail", "Sendmail"},

		// No suffix at all.
		{"plain name", "Invoice", "Invoice"},
		{"plain snake name", "order_confirmation", "OrderConfirmation"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toMailName(tt.in); got != tt.want {
				t.Errorf("toMailName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
