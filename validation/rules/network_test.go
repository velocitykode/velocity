package rules

import "testing"

func TestIPRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"nil ok", nil, false},
		{"ipv4", "192.168.1.1", false},
		{"ipv6", "::1", false},
		{"empty", "", true},
		{"garbage", "not-an-ip", true},
		{"non-string", 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := IPRule("ip", tt.value, nil, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestIPv4Rule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"nil ok", nil, false},
		{"ipv4 pass", "10.0.0.1", false},
		{"ipv6 fails", "::1", true},
		{"garbage fails", "999.999.999.999", true},
		{"non-string fails", 4, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := IPv4Rule("ip", tt.value, nil, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestIPv6Rule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"nil ok", nil, false},
		{"ipv6 pass", "::1", false},
		{"ipv6 full", "2001:db8::1", false},
		{"ipv4 fails", "127.0.0.1", true},
		{"ipv4-mapped-ipv6 fails (parses as v4)", "::ffff:127.0.0.1", true},
		{"garbage fails", "not-an-ip", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := IPv6Rule("ip", tt.value, nil, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}
