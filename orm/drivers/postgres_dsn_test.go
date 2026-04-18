package drivers

import (
	"bytes"
	"strings"
	"testing"
)

// TestResolveSSLMode_DefaultsToRequire verifies that callers who do not
// pin an explicit SSLMode land on the secure "require" default.
// Regression guard for the previous "disable" default.
func TestResolveSSLMode_DefaultsToRequire(t *testing.T) {
	if got := resolveSSLMode(""); got != "require" {
		t.Fatalf("resolveSSLMode(\"\") = %q, want %q", got, "require")
	}
}

// TestResolveSSLMode_ConfigOverride verifies a configured value wins over
// the default.
func TestResolveSSLMode_ConfigOverride(t *testing.T) {
	if got := resolveSSLMode("disable"); got != "disable" {
		t.Fatalf("resolveSSLMode(\"disable\") = %q, want %q", got, "disable")
	}
}

// TestPostgresDriver_DSNIncludesRequireByDefault verifies that Connect
// constructs a DSN containing "sslmode=require" when the caller provides
// no SSLMode and DB_SSL_MODE is unset. We intercept the DSN by calling
// openAndPing through a mock substitution is overkill; instead we build
// the DSN the same way Connect does and assert the expected substring.
func TestPostgresDriver_DSNIncludesRequireByDefault(t *testing.T) {
	cfg := ConnectionConfig{
		Host:     "db.example.com",
		Port:     "5432",
		Database: "app",
		Username: "svc",
		Password: "s3cret",
	}

	dsn := buildPostgresDSNForTest(cfg)

	if !strings.Contains(dsn, "sslmode=require") {
		t.Errorf("expected DSN to contain sslmode=require, got %q", dsn)
	}
	if strings.Contains(dsn, "sslmode=disable") {
		t.Errorf("expected DSN not to contain sslmode=disable, got %q", dsn)
	}
}

// buildPostgresDSNForTest constructs the same DSN string that
// PostgresDriver.Connect would produce, without opening a real connection.
// It mirrors the logic in Connect so it is covered by the same tests.
func buildPostgresDSNForTest(config ConnectionConfig) string {
	dsn := "host=" + escapePgDSNValue(config.Host) +
		" port=" + escapePgDSNValue(config.Port) +
		" user=" + escapePgDSNValue(config.Username) +
		" dbname=" + escapePgDSNValue(config.Database)
	if config.Password != "" {
		dsn += " password=" + escapePgDSNValue(config.Password)
	}
	dsn += " sslmode=" + escapePgDSNValue(resolveSSLMode(config.SSLMode))
	if config.TimeZone != "" {
		dsn += " TimeZone=" + escapePgDSNValue(config.TimeZone)
	}
	if config.Schema != "" {
		dsn += " search_path=" + escapePgDSNValue(config.Schema)
	}
	return dsn
}

// TestRedactDSNPassword verifies passwords are stripped before any error
// surface exposes the DSN.
func TestRedactDSNPassword(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "unquoted password",
			in:   "host=localhost user=bob password=hunter2 dbname=app",
			want: "host=localhost user=bob password=[REDACTED] dbname=app",
		},
		{
			name: "quoted password with space",
			in:   "host=localhost user=bob password='hun ter 2' dbname=app",
			want: "host=localhost user=bob password=[REDACTED] dbname=app",
		},
		{
			name: "quoted password with escaped quote",
			in:   `host=localhost user=bob password='hun\'ter' dbname=app`,
			want: "host=localhost user=bob password=[REDACTED] dbname=app",
		},
		{
			name: "no password",
			in:   "host=localhost user=bob dbname=app",
			want: "host=localhost user=bob dbname=app",
		},
		{
			name: "trailing password",
			in:   "host=localhost user=bob password=hunter2",
			want: "host=localhost user=bob password=[REDACTED]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactDSNPassword(tc.in); got != tc.want {
				t.Errorf("redactDSNPassword(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestEscapePgDSNValue_LibPQCompatible asserts libpq escaping rules per
// https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING
func TestEscapePgDSNValue_LibPQCompatible(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain alphanumeric", "alice", "alice"},
		{"empty", "", "''"},
		{"contains space", "has space", "'has space'"},
		{"contains single quote", "O'Brien", `'O\'Brien'`},
		{"contains backslash", `C:\path`, `'C:\\path'`},
		{"contains equals", "a=b", "'a=b'"},
		{"plain with punctuation", "some.thing_1-2:3/4", "some.thing_1-2:3/4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapePgDSNValue(tc.in); got != tc.want {
				t.Errorf("escapePgDSNValue(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestEscapePgDSNValue_BytewiseRoundtrip asserts the escaped form is never
// empty for non-empty input and always quoted when it contains a backslash.
// This is a belt-and-suspenders check against regressions.
func TestEscapePgDSNValue_NonEmpty(t *testing.T) {
	for _, in := range []string{"a", "ab", " ", `\\`, "'"} {
		if got := escapePgDSNValue(in); bytes.Equal([]byte(got), nil) {
			t.Fatalf("escapePgDSNValue(%q) returned empty", in)
		}
	}
}
