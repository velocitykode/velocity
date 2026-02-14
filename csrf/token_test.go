package csrf

import (
	"testing"
)

func TestGenerateToken(t *testing.T) {
	// Generate tokens
	token1, err1 := GenerateToken()
	token2, err2 := GenerateToken()

	// Check for errors
	if err1 != nil {
		t.Fatalf("Failed to generate token 1: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("Failed to generate token 2: %v", err2)
	}

	// Tokens should not be empty
	if token1 == "" {
		t.Error("Token 1 is empty")
	}
	if token2 == "" {
		t.Error("Token 2 is empty")
	}

	// Tokens should be unique
	if token1 == token2 {
		t.Error("Generated tokens are not unique")
	}

	// Tokens should have reasonable length (base64 encoding of 32 bytes)
	if len(token1) < 40 {
		t.Errorf("Token length too short: %d", len(token1))
	}
}

func TestGenerateTokenUniqueness(t *testing.T) {
	// Generate many tokens and check for uniqueness
	tokens := make(map[string]bool)
	count := 1000

	for i := 0; i < count; i++ {
		token, err := GenerateToken()
		if err != nil {
			t.Fatalf("Failed to generate token: %v", err)
		}

		if tokens[token] {
			t.Errorf("Duplicate token generated: %s", token)
		}
		tokens[token] = true
	}

	if len(tokens) != count {
		t.Errorf("Expected %d unique tokens, got %d", count, len(tokens))
	}
}

func TestValidateToken(t *testing.T) {
	tests := []struct {
		name   string
		token1 string
		token2 string
		want   bool
	}{
		{
			name:   "identical tokens",
			token1: "abc123def456",
			token2: "abc123def456",
			want:   true,
		},
		{
			name:   "different tokens",
			token1: "abc123def456",
			token2: "xyz789ghi012",
			want:   false,
		},
		{
			name:   "different lengths",
			token1: "abc123",
			token2: "abc123def",
			want:   false,
		},
		{
			name:   "empty tokens",
			token1: "",
			token2: "",
			want:   true,
		},
		{
			name:   "one empty token",
			token1: "abc123",
			token2: "",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateToken(tt.token1, tt.token2)
			if got != tt.want {
				t.Errorf("ValidateToken() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateTokenConstantTime(t *testing.T) {
	// This test ensures ValidateToken uses constant-time comparison
	// We can't easily measure timing in a test, but we can verify
	// that it handles various inputs correctly

	token1, _ := GenerateToken()
	token2, _ := GenerateToken()

	// Same token should validate
	if !ValidateToken(token1, token1) {
		t.Error("Same token should validate to true")
	}

	// Different tokens should not validate
	if ValidateToken(token1, token2) {
		t.Error("Different tokens should validate to false")
	}

	// Partial matches should not validate
	if len(token1) > 5 {
		partial := token1[:len(token1)-5]
		if ValidateToken(token1, partial) {
			t.Error("Partial token match should validate to false")
		}
	}
}

func BenchmarkGenerateToken(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := GenerateToken()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkValidateToken(b *testing.B) {
	token1 := "abc123def456ghi789jkl012mno345pqr678"
	token2 := "abc123def456ghi789jkl012mno345pqr678"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ValidateToken(token1, token2)
	}
}
