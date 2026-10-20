package velocity

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Every envReader parser: unset or empty takes the default with no error;
// a set value that parses is used; a set value that does not parse keeps
// the default and records one error naming the key and the value.
func TestEnvReader_Parsers(t *testing.T) {
	const key = "VELOCITY_TEST_ENV_READER"
	cases := []struct {
		name    string
		raw     string
		read    func(r *EnvReader) any
		want    any
		wantErr bool
	}{
		{"int unset", "", func(r *EnvReader) any { return r.Int(key, 7) }, 7, false},
		{"int set", " 42 ", func(r *EnvReader) any { return r.Int(key, 7) }, 42, false},
		{"int bad", "4x", func(r *EnvReader) any { return r.Int(key, 7) }, 7, true},
		{"int64 bad", "1.5", func(r *EnvReader) any { return r.Int64(key, 9) }, int64(9), true},
		{"positive zero", "0", func(r *EnvReader) any { return r.PositiveInt(key) }, 0, true},
		{"positive negative", "-3", func(r *EnvReader) any { return r.PositiveInt(key) }, 0, true},
		{"positive set", "5", func(r *EnvReader) any { return r.PositiveInt(key) }, 5, false},
		{"duration set", "1m30s", func(r *EnvReader) any { return r.Duration(key, time.Second) }, 90 * time.Second, false},
		{"duration bare number", "60", func(r *EnvReader) any { return r.Duration(key, time.Second) }, time.Second, true},
		{"bool unset keeps true", "", func(r *EnvReader) any { return r.Bool(key, true) }, true, false},
		{"bool TRUE", "TRUE", func(r *EnvReader) any { return r.Bool(key, false) }, true, false},
		{"bool on", "on", func(r *EnvReader) any { return r.Bool(key, false) }, true, false},
		{"bool 0", "0", func(r *EnvReader) any { return r.Bool(key, true) }, false, false},
		{"bool Off", "Off", func(r *EnvReader) any { return r.Bool(key, true) }, false, false},
		{"bool maybe", "maybe", func(r *EnvReader) any { return r.Bool(key, true) }, true, true},
		{"samesite unset", "", func(r *EnvReader) any { return r.sameSite(key) }, http.SameSiteLaxMode, false},
		{"samesite Strict", "Strict", func(r *EnvReader) any { return r.sameSite(key) }, http.SameSiteStrictMode, false},
		{"samesite none", " none ", func(r *EnvReader) any { return r.sameSite(key) }, http.SameSiteNoneMode, false},
		{"samesite typo", "strictt", func(r *EnvReader) any { return r.sameSite(key) }, http.SameSiteLaxMode, true},
		{"decay zero", "0", func(r *EnvReader) any { return r.loginDecay(key) }, time.Duration(0), true},
		{"decay seconds", "90", func(r *EnvReader) any { return r.loginDecay(key) }, 90 * time.Second, false},
		{"grpc size zero", "0", func(r *EnvReader) any { return r.grpcMsgSize(key) }, 0, true},
		{"port set", "5433", func(r *EnvReader) any { return r.Port(key, "5432") }, "5433", false},
		{"port bad", "54x", func(r *EnvReader) any { return r.Port(key, "5432") }, "5432", true},
		{"port out of range", "70000", func(r *EnvReader) any { return r.Port(key, "5432") }, "5432", true},
		{"index negative", "-1", func(r *EnvReader) any { return r.index(key, "0") }, "0", true},
		{"index set", "3", func(r *EnvReader) any { return r.index(key, "0") }, "3", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(key, tc.raw)
			r := &EnvReader{}
			if got := tc.read(r); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			if !tc.wantErr {
				if len(r.errs) != 0 {
					t.Fatalf("unexpected errors: %v", r.errs)
				}
				return
			}
			if len(r.errs) != 1 {
				t.Fatalf("errors = %v, want exactly one", r.errs)
			}
			err := r.errs[0]
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error %v does not wrap ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), key+"=") {
				t.Fatalf("error %v does not name the key", err)
			}
		})
	}
}

// ConfigFromEnv collects every bad value, and Validate (run by New)
// reports all of them in one error.
func TestConfigFromEnv_ReportsEveryBadValueAtOnce(t *testing.T) {
	t.Setenv("SERVER_WRITE_TIMEOUT", "60")
	t.Setenv("SESSION_SAME_SITE", "strictt")
	t.Setenv("LOG_REDACT", "maybe")
	t.Setenv("CACHE_DRIVER", "redsi")

	err := ConfigFromEnv().Validate()
	if err == nil {
		t.Fatal("Validate accepted bad env values")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error %v does not wrap ErrInvalidConfig", err)
	}
	for _, want := range []string{`SERVER_WRITE_TIMEOUT="60"`, `SESSION_SAME_SITE="strictt"`, `LOG_REDACT="maybe"`, `"redsi"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

// Err joins every bad value the reader saw, each naming its key; a reader
// that saw none returns nil.
func TestEnvReader_Err(t *testing.T) {
	t.Setenv("VELOCITY_TEST_APP_PAGE_SIZE", "lots")
	t.Setenv("VELOCITY_TEST_APP_BETA", "perhaps")
	t.Setenv("VELOCITY_TEST_APP_ORIGINS", " https://a.example , https://b.example ")

	r := NewEnvReader()
	if got := r.List("VELOCITY_TEST_APP_ORIGINS"); len(got) != 2 || got[1] != "https://b.example" {
		t.Fatalf("List = %q", got)
	}
	if err := r.Err(); err != nil {
		t.Fatalf("Err after only good reads = %v", err)
	}
	if got := r.Int("VELOCITY_TEST_APP_PAGE_SIZE", 20); got != 20 {
		t.Fatalf("Int = %d, want the default 20", got)
	}
	r.Bool("VELOCITY_TEST_APP_BETA", false)
	err := r.Err()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Err = %v, want ErrInvalidConfig", err)
	}
	for _, key := range []string{"VELOCITY_TEST_APP_PAGE_SIZE", "VELOCITY_TEST_APP_BETA"} {
		if !strings.Contains(err.Error(), key+"=") {
			t.Errorf("Err does not name %s: %v", key, err)
		}
	}
}
