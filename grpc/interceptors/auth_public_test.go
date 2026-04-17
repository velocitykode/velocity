package interceptors

import "testing"

// TestIsPublicMethod_ExactAndPrefix covers Task 8b: "/admin" must not match
// "/administrator/…" — entries are either exact or explicitly prefix-style
// (trailing "/").
func TestIsPublicMethod_ExactAndPrefix(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		list     []string
		expected bool
	}{
		{"exact match", "/health.Check", []string{"/health.Check"}, true},
		{"exact mismatch", "/health.Check/other", []string{"/health.Check"}, false},
		{"prefix with trailing slash matches", "/grpc.health.v1.Health/Watch", []string{"/grpc.health.v1.Health/"}, true},
		{"prefix without trailing slash must not partial-match",
			"/administrator.Service/DoDangerous", []string{"/admin"}, false},
		{"prefix without trailing slash requires exact",
			"/admin", []string{"/admin"}, true},
		{"empty entry is skipped", "/svc/X", []string{"", "/svc/"}, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPublicMethod(c.method, c.list); got != c.expected {
				t.Errorf("isPublicMethod(%q, %v) = %v, want %v", c.method, c.list, got, c.expected)
			}
		})
	}
}
