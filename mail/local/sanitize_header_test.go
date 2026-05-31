package local

import "testing"

func TestSanitizeHeader_DropsC0Controls(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Subject: hello", "Subject: hello"},
		{"cr", "a\rb", "ab"},
		{"lf", "a\nb", "ab"},
		{"crlf injection", "Alice <alice@example.com>\r\nBcc: attacker@evil.com", "Alice <alice@example.com>Bcc: attacker@evil.com"},
		{"nul truncation", "Bob\x00<bob@example.com>", "Bob<bob@example.com>"},
		{"tab preserved", "Alice\talice@example.com", "Alice\talice@example.com"},
		{"esc", "a\x1bb", "ab"},
		{"bell", "a\x07b", "ab"},
		{"del", "a\x7fb", "ab"},
		{"utf8 preserved", "Ålice <a@ex.com>", "Ålice <a@ex.com>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeHeader(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeHeader(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
