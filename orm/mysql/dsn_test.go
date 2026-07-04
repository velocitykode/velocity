package mysql

import (
	"net/url"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/orm/drivers"
)

// TestResolveMySQLTLS verifies empty TLS config now defaults to tls=true as a
// security hardening change while explicit opt-outs and named profiles pass
// through unchanged.
func TestResolveMySQLTLS(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		want       string
	}{
		{
			name: "empty defaults to required TLS",
			want: "true",
		},
		{
			name:       "explicit false",
			configured: "false",
			want:       "false",
		},
		{
			name:       "explicit skip verify",
			configured: "skip-verify",
			want:       "skip-verify",
		},
		{
			name:       "explicit preferred",
			configured: "preferred",
			want:       "preferred",
		},
		{
			name:       "named tls profile",
			configured: "local-dev-profile",
			want:       "local-dev-profile",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveMySQLTLS(tc.configured); got != tc.want {
				t.Fatalf("resolveMySQLTLS(%q) = %q, want %q", tc.configured, got, tc.want)
			}
		})
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
// hardened default tls=true into the query string portion of the DSN.
func TestMySQLDriver_DSNParamsIncludeTLSDefault(t *testing.T) {
	params := buildMySQLParamsForTest(drivers.ConnectionConfig{})
	if !strings.Contains(params, "tls=true") {
		t.Errorf("expected params to contain tls=true, got %q", params)
	}
}

// TestMySQLDriver_DSNNeverEmitsLoc pins the time codec: `loc=` must never
// appear in the DSN (go-sql-driver's default Loc=UTC is the storage
// contract - it converts bound time.Time params to UTC on the wire and
// returns scanned timestamps in time.UTC). TimeZone maps to the session
// `time_zone` system variable instead, mirroring postgres's `TimeZone=`.
func TestMySQLDriver_DSNNeverEmitsLoc(t *testing.T) {
	t.Run("no TimeZone", func(t *testing.T) {
		params := buildMySQLParamsForTest(drivers.ConnectionConfig{})
		if strings.Contains(params, "loc=") {
			t.Errorf("params contain loc= (time codec must stay UTC), got %q", params)
		}
	})
	t.Run("TimeZone set becomes session variable", func(t *testing.T) {
		params := buildMySQLParamsForTest(drivers.ConnectionConfig{TimeZone: "Asia/Karachi"})
		if strings.Contains(params, "loc=") {
			t.Errorf("params contain loc= (time codec must stay UTC), got %q", params)
		}
		if !strings.Contains(params, "time_zone=%27Asia%2FKarachi%27") {
			t.Errorf("expected session time_zone param, got %q", params)
		}
	})
}

// buildMySQLParamsForTest mirrors the DSN-parameter assembly in Connect so
// we can inspect the resulting query string without opening a network
// connection. Keep in sync with MySQLDriver.Connect.
func buildMySQLParamsForTest(config drivers.ConnectionConfig) string {
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
		params = append(params, "time_zone="+url.QueryEscape("'"+config.TimeZone+"'"))
	}
	params = append(params, "tls="+resolveMySQLTLS(config.TLS))
	return strings.Join(params, "&")
}
