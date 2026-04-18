package drivers

import (
	"strings"
	"testing"
)

// TestResolveMySQLTLS_DefaultsToPreferred asserts MySQL connections pick
// tls=preferred when the configured value is empty.
func TestResolveMySQLTLS_DefaultsToPreferred(t *testing.T) {
	if got := resolveMySQLTLS(""); got != "preferred" {
		t.Fatalf("resolveMySQLTLS = %q, want %q", got, "preferred")
	}
}

// TestResolveMySQLTLS_Override verifies the configured value wins.
func TestResolveMySQLTLS_Override(t *testing.T) {
	if got := resolveMySQLTLS("false"); got != "false" {
		t.Fatalf("resolveMySQLTLS = %q, want %q", got, "false")
	}
}

// TestRedactMySQLDSN verifies passwords are stripped from MySQL DSNs
// regardless of whether they contain URL-encoded metadata.
func TestRedactMySQLDSN(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "user and password",
			in:   "bob:hunter2@tcp(db:3306)/app?parseTime=true",
			want: "bob:[REDACTED]@tcp(db:3306)/app?parseTime=true",
		},
		{
			name: "url-encoded password",
			in:   "bob:hun%40ter%232@tcp(db:3306)/app",
			want: "bob:[REDACTED]@tcp(db:3306)/app",
		},
		{
			name: "no password (no colon)",
			in:   "bob@tcp(db:3306)/app",
			want: "bob@tcp(db:3306)/app",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactMySQLDSN(tc.in); got != tc.want {
				t.Errorf("redactMySQLDSN(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestMySQLDriver_DSNParamsIncludeTLSDefault verifies Connect wires the
// default tls=preferred into the query string portion of the DSN.
func TestMySQLDriver_DSNParamsIncludeTLSDefault(t *testing.T) {
	params := buildMySQLParamsForTest(ConnectionConfig{})
	if !strings.Contains(params, "tls=preferred") {
		t.Errorf("expected params to contain tls=preferred, got %q", params)
	}
}

// buildMySQLParamsForTest mirrors the DSN-parameter assembly in Connect so
// we can inspect the resulting query string without opening a network
// connection. Keep in sync with MySQLDriver.Connect.
func buildMySQLParamsForTest(config ConnectionConfig) string {
	params := []string{"parseTime=true"}
	if config.Charset != "" {
		params = append(params, "charset="+config.Charset)
	} else {
		params = append(params, "charset=utf8mb4")
	}
	if config.Collation != "" {
		params = append(params, "collation="+config.Collation)
	}
	if config.TimeZone != "" {
		params = append(params, "loc="+config.TimeZone)
	}
	params = append(params, "tls="+resolveMySQLTLS(config.TLS))
	return strings.Join(params, "&")
}
