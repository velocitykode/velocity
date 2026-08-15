package crypto

import "testing"

func TestEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []byte
		want bool
	}{
		{"equal", []byte("secret"), []byte("secret"), true},
		{"different", []byte("secret"), []byte("secreT"), false},
		{"different length", []byte("secret"), []byte("secre"), false},
		{"both empty", []byte{}, []byte{}, true},
		{"nil vs empty", nil, []byte{}, true},
		{"empty vs value", []byte{}, []byte("x"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Equal(tt.a, tt.b); got != tt.want {
				t.Errorf("Equal(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestEqualString(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"equal", "token", "token", true},
		{"different", "token", "Token", false},
		{"different length", "token", "toke", false},
		{"both empty", "", "", true},
		{"empty vs value", "", "x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EqualString(tt.a, tt.b); got != tt.want {
				t.Errorf("EqualString(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
