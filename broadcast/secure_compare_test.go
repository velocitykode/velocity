package broadcast

import "testing"

func TestSecureCompareToken(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"equal", "tok_abc123", "tok_abc123", true},
		{"differ one byte", "tok_abc123", "tok_abc124", false},
		{"differ length", "tok_abc", "tok_abc123", false},
		{"empty both", "", "", true},
		{"empty one", "tok", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SecureCompareToken(tc.a, tc.b); got != tc.want {
				t.Errorf("SecureCompareToken(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
