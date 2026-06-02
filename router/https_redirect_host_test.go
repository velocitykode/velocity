package router

import "testing"

func TestHTTPSRedirectHost(t *testing.T) {
	tests := []struct {
		name        string
		requested   string
		cfg         *httpsRedirectConfig
		routerHosts []string
		want        string
	}{
		{
			name:      "no allowlist reflects requested host (documented residual)",
			requested: "evil.com",
			cfg:       &httpsRedirectConfig{},
			want:      "evil.com",
		},
		{
			name:      "middleware allowlist permits listed host",
			requested: "good.com",
			cfg:       &httpsRedirectConfig{allowedHosts: map[string]bool{"good.com": true}, firstAllowedHost: "good.com"},
			want:      "good.com",
		},
		{
			name:      "middleware allowlist rejects unlisted host, prefers canonical",
			requested: "evil.com",
			cfg:       &httpsRedirectConfig{allowedHosts: map[string]bool{"good.com": true}, firstAllowedHost: "good.com", canonicalHost: "canon.com"},
			want:      "canon.com",
		},
		{
			name:      "middleware allowlist rejects unlisted host, falls back to first allowed",
			requested: "evil.com",
			cfg:       &httpsRedirectConfig{allowedHosts: map[string]bool{"good.com": true}, firstAllowedHost: "good.com"},
			want:      "good.com",
		},
		{
			name:        "router-level allowlist permits listed host without middleware config",
			requested:   "api.good.com",
			cfg:         &httpsRedirectConfig{},
			routerHosts: []string{"api.good.com"},
			want:        "api.good.com",
		},
		{
			name:        "router-level allowlist rejects unlisted host, falls back to first router host",
			requested:   "evil.com",
			cfg:         &httpsRedirectConfig{},
			routerHosts: []string{"api.good.com"},
			want:        "api.good.com",
		},
		{
			name:      "host match is case-insensitive",
			requested: "GOOD.com",
			cfg:       &httpsRedirectConfig{allowedHosts: map[string]bool{"good.com": true}, firstAllowedHost: "good.com"},
			want:      "GOOD.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := httpsRedirectHost(tt.requested, tt.cfg, tt.routerHosts)
			if got != tt.want {
				t.Errorf("httpsRedirectHost(%q) = %q, want %q", tt.requested, got, tt.want)
			}
		})
	}
}
