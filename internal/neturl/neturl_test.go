package neturl

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestIsPrivateOrInternal(t *testing.T) {
	cases := []struct {
		name    string
		ip      string
		private bool
	}{
		{"loopback v4", "127.0.0.1", true},
		{"loopback v6", "::1", true},
		{"unspecified v4", "0.0.0.0", true},
		{"unspecified v6", "::", true},
		{"rfc1918 10", "10.1.2.3", true},
		{"rfc1918 172.16", "172.16.5.5", true},
		{"rfc1918 192.168", "192.168.10.10", true},
		{"link-local v4", "169.254.1.1", true},
		{"metadata v4", "169.254.169.254", true},
		{"cgnat", "100.64.1.1", true},
		{"fc00", "fc00::1", true},
		{"public v4", "8.8.8.8", false},
		{"public v6", "2606:4700:4700::1111", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("parse %q", tc.ip)
			}
			got := IsPrivateOrInternal(ip)
			if got != tc.private {
				t.Errorf("IsPrivateOrInternal(%s) = %v, want %v", tc.ip, got, tc.private)
			}
		})
	}
}

func TestIsMetadataIP(t *testing.T) {
	if !IsMetadataIP(net.ParseIP("169.254.169.254")) {
		t.Error("expected 169.254.169.254 to be a metadata IP")
	}
	if IsMetadataIP(net.ParseIP("169.254.1.1")) {
		t.Error("link-local but not metadata should return false")
	}
	if IsMetadataIP(nil) {
		t.Error("nil ip should return false")
	}
}

func TestIsPrivateHost_IPLiteral(t *testing.T) {
	ctx := context.Background()
	private, err := IsPrivateHost(ctx, nil, "127.0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !private {
		t.Error("127.0.0.1 must be private")
	}

	private, err = IsPrivateHost(ctx, nil, "::1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !private {
		t.Error("::1 must be private")
	}

	private, err = IsPrivateHost(ctx, nil, "8.8.8.8")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if private {
		t.Error("8.8.8.8 must not be private")
	}
}

func TestIsPrivateHost_Localhost(t *testing.T) {
	ctx := context.Background()
	private, err := IsPrivateHost(ctx, nil, "localhost")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !private {
		t.Error("localhost must be private without DNS resolution")
	}
	private, err = IsPrivateHost(ctx, nil, "foo.localhost")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !private {
		t.Error("foo.localhost must be private")
	}
}

func TestValidateURLHost_MetadataIP(t *testing.T) {
	err := ValidateURLHost(context.Background(), nil, "http://169.254.169.254/latest/meta-data/")
	if err == nil {
		t.Fatal("metadata IP must be rejected")
	}
	if !errors.Is(err, ErrPrivateHost) {
		t.Errorf("expected ErrPrivateHost, got %v", err)
	}
}

func TestValidateURLHost_Loopback(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1/", "http://[::1]/", "http://localhost/"} {
		err := ValidateURLHost(context.Background(), nil, u)
		if err == nil {
			t.Errorf("%s must be rejected", u)
			continue
		}
		if !errors.Is(err, ErrPrivateHost) {
			t.Errorf("%s: expected ErrPrivateHost, got %v", u, err)
		}
	}
}

func TestValidateURLHost_BadScheme(t *testing.T) {
	err := ValidateURLHost(context.Background(), nil, "file:///etc/passwd")
	if err == nil {
		t.Fatal("file scheme must be rejected")
	}
	if errors.Is(err, ErrPrivateHost) {
		t.Errorf("bad-scheme error must not masquerade as ErrPrivateHost: %v", err)
	}
}

func TestETLDPlusOne(t *testing.T) {
	cases := map[string]string{
		"example.com":          "example.com",
		"www.example.com":      "example.com",
		"a.b.c.example.com":    "example.com",
		"evil.co.uk":           "co.uk", // best-effort
		"127.0.0.1":            "127.0.0.1",
		"[::1]":                "::1",
		"api.example.com:8080": "example.com",
	}
	for in, want := range cases {
		if got := ETLDPlusOne(in); got != want {
			t.Errorf("ETLDPlusOne(%q) = %q, want %q", in, got, want)
		}
	}
}
