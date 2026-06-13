package console

import "testing"

func TestNormalizeServeEnv(t *testing.T) {
	tests := []struct {
		name          string
		in            string
		wantEnv       string
		wantDefaulted bool
	}{
		{"empty defaults to development", "", "development", true},
		{"explicit production is preserved", "production", "production", false},
		{"explicit development is not flagged as defaulted", "development", "development", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, defaulted := normalizeServeEnv(tt.in)
			if env != tt.wantEnv {
				t.Errorf("normalizeServeEnv(%q) env = %q, want %q", tt.in, env, tt.wantEnv)
			}
			if defaulted != tt.wantDefaulted {
				t.Errorf("normalizeServeEnv(%q) defaulted = %v, want %v", tt.in, defaulted, tt.wantDefaulted)
			}
		})
	}
}
