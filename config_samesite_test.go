package velocity

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestParseSameSiteStrict(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    http.SameSite
		wantErr bool
	}{
		{name: "empty defaults to lax", value: "", want: http.SameSiteLaxMode},
		{name: "strict", value: "strict", want: http.SameSiteStrictMode},
		{name: "lax", value: "lax", want: http.SameSiteLaxMode},
		{name: "none", value: "none", want: http.SameSiteNoneMode},
		{name: "capitalized rejected", value: "Strict", want: http.SameSiteLaxMode, wantErr: true},
		{name: "typo rejected", value: "strictt", want: http.SameSiteLaxMode, wantErr: true},
		{name: "attribute syntax rejected", value: "samesite=strict", want: http.SameSiteLaxMode, wantErr: true},
		{name: "surrounding whitespace rejected", value: "  strict  ", want: http.SameSiteLaxMode, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSameSiteStrict("SESSION_SAME_SITE", tt.value)
			if got != tt.want {
				t.Fatalf("SameSite: got %v, want %v", got, tt.want)
			}
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if !errors.Is(err, ErrInvalidConfig) {
					t.Fatalf("error should wrap ErrInvalidConfig; got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestConfigValidateRejectsInvalidSessionSameSite(t *testing.T) {
	cfg := Config{sessionSameSiteRaw: "Strict"}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected invalid SESSION_SAME_SITE to fail validation")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error should wrap ErrInvalidConfig; got %v", err)
	}
}

func TestConfigValidateRejectsInvalidCSRFSameSite(t *testing.T) {
	cfg := Config{csrfSameSiteRaw: "samesite=strict"}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected invalid CSRF_SAME_SITE to fail validation")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error should wrap ErrInvalidConfig; got %v", err)
	}
}

func TestConfigValidateAcceptsValidSameSiteValues(t *testing.T) {
	cfg := Config{
		sessionSameSiteRaw: "strict",
		csrfSameSiteRaw:    "none",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid SameSite values should pass validation: %v", err)
	}
}

func TestConfigValidateAcceptsEmptySameSiteValues(t *testing.T) {
	cfg := Config{}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("empty SameSite values should default to Lax without validation error: %v", err)
	}
}

func TestConfigFromEnvThreadsSameSiteRawValuesIntoValidate(t *testing.T) {
	tests := []struct {
		name string
		env  string
		key  string
	}{
		{name: "session", env: "SESSION_SAME_SITE", key: "SESSION_SAME_SITE"},
		{name: "csrf", env: "CSRF_SAME_SITE", key: "CSRF_SAME_SITE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SESSION_SAME_SITE", "")
			t.Setenv("CSRF_SAME_SITE", "")
			t.Setenv(tt.env, "strictt")

			err := ConfigFromEnv().Validate()
			if err == nil {
				t.Fatalf("expected %s typo to fail validation", tt.env)
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error should wrap ErrInvalidConfig; got %v", err)
			}
			if !strings.Contains(err.Error(), tt.key+`="strictt"`) {
				t.Fatalf("error should name offending env var and value; got %v", err)
			}
		})
	}
}
