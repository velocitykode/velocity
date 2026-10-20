package velocity

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

func TestConfigFromEnv_UnsetAppEnvStaysEmptyAndFailsClosed(t *testing.T) {
	t.Setenv("APP_ENV", "")
	_ = os.Unsetenv("APP_ENV")

	cfg := ConfigFromEnv()
	if cfg.Env != "" {
		t.Fatalf("unset APP_ENV should stay empty, got %q", cfg.Env)
	}
	if contract.IsDevOrTestEnv(cfg.Env) {
		t.Fatalf("unset APP_ENV must not be classified as dev/test")
	}
	if contract.IsTestingEnv(cfg.Env) {
		t.Fatalf("unset APP_ENV must not be classified as testing")
	}
}

// TestConfigFromEnv_DBTimezoneAndAppTimezone pins the two timezone knobs:
// DB_TIMEZONE flows to orm.Config.TimeZone (database SESSION timezone only),
// APP_TIMEZONE flows to Config.Timezone (presentation) and defaults to UTC.
func TestConfigFromEnv_DBTimezoneAndAppTimezone(t *testing.T) {
	t.Setenv("DB_TIMEZONE", "Asia/Karachi")
	t.Setenv("APP_TIMEZONE", "")
	_ = os.Unsetenv("APP_TIMEZONE")

	cfg := ConfigFromEnv()
	if cfg.DB.TimeZone != "Asia/Karachi" {
		t.Errorf("DB.TimeZone = %q, want Asia/Karachi", cfg.DB.TimeZone)
	}
	if cfg.Timezone != "UTC" {
		t.Errorf("Timezone default = %q, want UTC", cfg.Timezone)
	}

	t.Setenv("APP_TIMEZONE", "Asia/Karachi")
	if got := ConfigFromEnv().Timezone; got != "Asia/Karachi" {
		t.Errorf("Timezone = %q, want Asia/Karachi", got)
	}
}

// TestConfigFromEnv_CryptoFlags pins CRYPTO_DISABLE_V0 and CRYPTO_DEBUG to
// the one boolean parser: any case of 1/true/yes/on is on, 0/false/no/off
// or unset is off, and anything else is a Validate error.
func TestConfigFromEnv_CryptoFlags(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "yes": true, "TRUE": true, "True": true, "": false, "0": false, "off": false} {
		t.Setenv("CRYPTO_DISABLE_V0", v)
		t.Setenv("CRYPTO_DEBUG", v)
		cfg := ConfigFromEnv()
		if c := cfg.Crypto; c.DisableV0 != want || c.Debug != want {
			t.Fatalf("value %q: DisableV0=%v Debug=%v, want both %v", v, c.DisableV0, c.Debug, want)
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("value %q: Validate = %v", v, err)
		}
	}
	t.Setenv("CRYPTO_DISABLE_V0", "maybe")
	t.Setenv("CRYPTO_DEBUG", "")
	if err := ConfigFromEnv().Validate(); err == nil || !strings.Contains(err.Error(), `CRYPTO_DISABLE_V0="maybe"`) {
		t.Fatalf("Validate = %v, want the bad CRYPTO_DISABLE_V0 named", err)
	}
}

// TestConfigFromEnv_QueueAcceptUnsignedTruthy pins the truthy spellings
// QUEUE_ACCEPT_UNSIGNED accepts.
func TestConfigFromEnv_QueueAcceptUnsignedTruthy(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, " YES ": true, "on": true, "": false, "false": false, "0": false} {
		t.Setenv("QUEUE_ACCEPT_UNSIGNED", v)
		if got := ConfigFromEnv().Queue.AcceptUnsigned; got != want {
			t.Fatalf("QUEUE_ACCEPT_UNSIGNED=%q: AcceptUnsigned = %v, want %v", v, got, want)
		}
	}
}

// TestConfigFromEnv_LogRedactFlags pins LOG_REDACT / LOG_REDACT_EMAILS onto
// the log config keys log.WrapWithRedactors reads.
func TestConfigFromEnv_LogRedactFlags(t *testing.T) {
	t.Setenv("LOG_REDACT", "")
	t.Setenv("LOG_REDACT_EMAILS", "")
	c := ConfigFromEnv().Log.Config
	if _, ok := c["redact"]; ok {
		t.Fatalf("unset LOG_REDACT set redact: %v", c["redact"])
	}
	if _, ok := c["redact_emails"]; ok {
		t.Fatalf("unset LOG_REDACT_EMAILS set redact_emails: %v", c["redact_emails"])
	}

	t.Setenv("LOG_REDACT", "TRUE")
	t.Setenv("LOG_REDACT_EMAILS", "true")
	c = ConfigFromEnv().Log.Config
	if c["redact"] != true || c["redact_emails"] != true {
		t.Fatalf("redact=%v redact_emails=%v, want true/true", c["redact"], c["redact_emails"])
	}
}

// TestConfigFromEnv_CacheDriver pins CACHE_DRIVER: unset is memory, any
// other value is passed through, and Validate rejects a name no cache
// driver is registered under.
func TestConfigFromEnv_CacheDriver(t *testing.T) {
	for raw, want := range map[string]string{"": "memory", "memory": "memory", "file": "file", "FILE": "file", " Redis ": "redis", "memcached": "memcached"} {
		t.Setenv("CACHE_DRIVER", raw)
		if got := ConfigFromEnv().Cache.DefaultDriver(); got != want {
			t.Fatalf("CACHE_DRIVER=%q: driver = %q, want %q", raw, got, want)
		}
	}
	t.Setenv("CACHE_DRIVER", "memcached")
	if err := ConfigFromEnv().Validate(); err == nil || !strings.Contains(err.Error(), `"memcached" not registered`) {
		t.Fatalf("Validate = %v, want the unknown cache driver named", err)
	}
}

// TestConfigFromEnv_LoginDecay pins AUTH_LOGIN_DECAY parsing: a positive
// Go duration or bare seconds, surrounding space trimmed; zero, negative
// or malformed keeps the default and is a Validate error.
func TestConfigFromEnv_LoginDecay(t *testing.T) {
	for raw, want := range map[string]time.Duration{"30s": 30 * time.Second, "7": 7 * time.Second, " 30s": 30 * time.Second, "0": 0, "-5": 0, "x": 0} {
		t.Setenv("AUTH_LOGIN_DECAY", raw)
		cfg := ConfigFromEnv()
		if got := cfg.Auth.LoginThrottle.Decay; got != want {
			t.Fatalf("AUTH_LOGIN_DECAY=%q: Decay = %v, want %v", raw, got, want)
		}
		if bad := want == 0; bad != (cfg.Validate() != nil) {
			t.Fatalf("AUTH_LOGIN_DECAY=%q: Validate error = %v, want error %v", raw, cfg.Validate(), bad)
		}
	}
}

// TestConfigFromEnv_Maintenance pins the maintenance keys: the root and the
// exclude list come from VELOCITY_MAINTENANCE_*, the bypass salt from APP_KEY.
func TestConfigFromEnv_Maintenance(t *testing.T) {
	t.Setenv("VELOCITY_MAINTENANCE_ROOT", "/srv/app")
	t.Setenv("VELOCITY_MAINTENANCE_EXCLUDE_PATHS", "/webhooks, /metrics")
	t.Setenv("APP_KEY", "k")
	m := ConfigFromEnv().Maintenance
	if m.Root != "/srv/app" || m.AppKey != "k" {
		t.Fatalf("Root=%q AppKey=%q, want /srv/app and k", m.Root, m.AppKey)
	}
	if len(m.ExcludePaths) != 2 || m.ExcludePaths[0] != "/webhooks" || m.ExcludePaths[1] != "/metrics" {
		t.Fatalf("ExcludePaths = %q, want the trimmed comma split", m.ExcludePaths)
	}

	t.Setenv("VELOCITY_MAINTENANCE_EXCLUDE_PATHS", "")
	if p := ConfigFromEnv().Maintenance.ExcludePaths; p != nil {
		t.Fatalf("empty VELOCITY_MAINTENANCE_EXCLUDE_PATHS: ExcludePaths = %q, want nil", p)
	}
}

// TestConfigFromEnv_GRPC pins the gRPC keys onto grpcconfig.Config,
// with message sizes that are not a positive integer reported by Validate.
func TestConfigFromEnv_GRPC(t *testing.T) {
	t.Setenv("APP_ENV", " Production ")
	t.Setenv("GRPC_PORT", "6000")
	t.Setenv("GRPC_REFLECTION", "true")
	t.Setenv("GATEWAY_PORT", "9000")
	t.Setenv("GRPC_ENDPOINT", "svc:6000")
	t.Setenv("GRPC_INSECURE", "true")
	t.Setenv("GRPC_MAX_RECV_SIZE", "abc")
	t.Setenv("GRPC_MAX_SEND_SIZE", "0")
	g := ConfigFromEnv().GRPC
	if g.ServerPort != "6000" || !g.EnableReflection || g.GatewayPort != "9000" || g.GRPCEndpoint != "svc:6000" || !g.Insecure {
		t.Fatalf("GRPC = %+v", g)
	}
	if g.Environment != "production" {
		t.Fatalf("Environment = %q, want the normalised APP_ENV", g.Environment)
	}
	if g.MaxRecvMsgSize != 0 || g.MaxSendMsgSize != 0 {
		t.Fatalf("sizes %d/%d, want 0/0 (the default) for bad values", g.MaxRecvMsgSize, g.MaxSendMsgSize)
	}
	err := ConfigFromEnv().Validate()
	for _, want := range []string{`GRPC_MAX_RECV_SIZE="abc"`, `GRPC_MAX_SEND_SIZE="0"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate = %v, want %s reported", err, want)
		}
	}

	t.Setenv("GRPC_MAX_RECV_SIZE", "-5")
	t.Setenv("GRPC_MAX_SEND_SIZE", "1024")
	t.Setenv("GRPC_INSECURE", "OFF")
	g = ConfigFromEnv().GRPC
	if g.MaxRecvMsgSize != 0 || g.MaxSendMsgSize != 1024 || g.Insecure {
		t.Fatalf("sizes %d/%d insecure %v, want 0/1024, false", g.MaxRecvMsgSize, g.MaxSendMsgSize, g.Insecure)
	}
}
