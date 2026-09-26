package contract

import (
	"net/http"
	"testing"
)

func TestCookiePolicy_Cookie(t *testing.T) {
	tests := []struct {
		name     string
		policy   CookiePolicy
		maxAge   int
		httpOnly bool
		want     http.Cookie
	}{
		{
			name:     "zero value is the secure default",
			policy:   CookiePolicy{},
			maxAge:   300,
			httpOnly: true,
			want:     http.Cookie{Path: "/", MaxAge: 300, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode},
		},
		{
			name:     "session attributes reach the cookie",
			policy:   NewCookiePolicy("/app", "example.test", true, http.SameSiteNoneMode),
			maxAge:   60,
			httpOnly: false,
			want:     http.Cookie{Path: "/app", Domain: "example.test", MaxAge: 60, Secure: true, SameSite: http.SameSiteNoneMode},
		},
		{
			name:     "empty path and zero SameSite normalise",
			policy:   NewCookiePolicy("", "", false, http.SameSiteDefaultMode),
			maxAge:   0,
			httpOnly: true,
			want:     http.Cookie{Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode},
		},
		{
			name:     "unset SameSite normalises",
			policy:   NewCookiePolicy("/", "", true, 0),
			maxAge:   1,
			httpOnly: true,
			want:     http.Cookie{Path: "/", MaxAge: 1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode},
		},
		{
			name:     "deletion keeps path and domain",
			policy:   NewCookiePolicy("/app", "example.test", false, http.SameSiteStrictMode),
			maxAge:   -1,
			httpOnly: true,
			want:     http.Cookie{Path: "/app", Domain: "example.test", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.policy.Cookie("name", "value", tt.maxAge, tt.httpOnly)
			if got.Name != "name" || got.Value != "value" {
				t.Errorf("identity: got %q=%q", got.Name, got.Value)
			}
			if got.Path != tt.want.Path || got.Domain != tt.want.Domain || got.MaxAge != tt.want.MaxAge ||
				got.HttpOnly != tt.want.HttpOnly || got.Secure != tt.want.Secure || got.SameSite != tt.want.SameSite {
				t.Errorf("got %#v, want %#v", *got, tt.want)
			}
			if got.Path != tt.policy.Path() || got.Domain != tt.policy.Domain() ||
				got.Secure != tt.policy.Secure() || got.SameSite != tt.policy.SameSite() {
				t.Errorf("cookie attributes disagree with the policy accessors: %#v", *got)
			}
		})
	}
}
