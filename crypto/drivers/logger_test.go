package drivers

import (
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	logdrivers "github.com/velocitykode/velocity/log/drivers"
)

var _ contract.LoggerAware = (*AESDriver)(nil)

// consoleTo returns a debug-level console logger writing to out.
func consoleTo(out *fallbacklogtest.Output) contract.Logger {
	return logdrivers.NewConsoleLoggerTo(out, 0)
}

// The first legacy v0 decrypt warns once through the driver's logger, and
// through the fallback logger when the driver has none; nothing goes
// through the standard library log.
func TestAESDriver_LegacyDecryptWarnsThroughItsLogger(t *testing.T) {
	const warning = "velocity/crypto: legacy v0 payload decrypted"

	t.Run("driver logger", func(t *testing.T) {
		stdlib := fallbacklogtest.CaptureStdlib(t)
		fallback := fallbacklogtest.Capture(t)
		out := &fallbacklogtest.Output{}
		d, err := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")
		if err != nil {
			t.Fatalf("NewAESDriver: %v", err)
		}
		d.SetLogger(consoleTo(out))
		legacy := handCraftLegacyCBC(t, d, []byte("old"))
		for i := 0; i < 2; i++ {
			if _, err := d.Decrypt(legacy); err != nil {
				t.Fatalf("Decrypt: %v", err)
			}
		}
		if got := strings.Count(out.String(), "WARN: "+warning); got != 1 {
			t.Errorf("driver logger warn lines = %d, want 1 (%q)", got, out.String())
		}
		if s := stdlib.String() + fallback.String(); s != "" {
			t.Errorf("stdlib / fallback got %q, want nothing", s)
		}
	})

	t.Run("no logger", func(t *testing.T) {
		stdlib := fallbacklogtest.CaptureStdlib(t)
		fallback := fallbacklogtest.Capture(t)
		d, err := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")
		if err != nil {
			t.Fatalf("NewAESDriver: %v", err)
		}
		if _, err := d.Decrypt(handCraftLegacyCBC(t, d, []byte("old"))); err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if got := fallback.Count("WARN", warning); got != 1 {
			t.Errorf("fallback warn lines = %d, want 1 (%q)", got, fallback.String())
		}
		if s := stdlib.String(); s != "" {
			t.Errorf("stdlib got %q, want nothing", s)
		}
	})
}

// With CRYPTO_DEBUG=true when the driver is built, a decrypt failure writes
// debug lines naming the failing stage (the key attempt's own stage, then
// the all-keys summary) through the driver's logger; without it, none. Key
// bytes and plaintext never appear.
func TestAESDriver_DecryptFailureDebugLine(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	for _, tc := range []struct {
		name  string
		debug string
		want  int
	}{
		{"CRYPTO_DEBUG=true", "true", 2},
		{"CRYPTO_DEBUG unset", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdlib := fallbacklogtest.CaptureStdlib(t)
			t.Setenv("CRYPTO_DEBUG", tc.debug)
			out := &fallbacklogtest.Output{}
			d, err := NewAESDriver(key, nil, "AES-256-GCM")
			if err != nil {
				t.Fatalf("NewAESDriver: %v", err)
			}
			d.SetLogger(consoleTo(out))
			sealed, err := d.Encrypt("secret plaintext")
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}
			other, err := NewAESDriver([]byte("fedcba9876543210fedcba9876543210"), nil, "AES-256-GCM")
			if err != nil {
				t.Fatalf("NewAESDriver: %v", err)
			}
			other.SetLogger(consoleTo(out))
			if _, err := other.Decrypt(sealed); err == nil {
				t.Fatal("Decrypt under the wrong key succeeded")
			}
			if got := strings.Count(out.String(), "DEBUG: velocity/crypto: decrypt failed"); got != tc.want {
				t.Errorf("debug lines = %d, want %d (%q)", got, tc.want, out.String())
			}
			if tc.want > 0 && (!strings.Contains(out.String(), "stage=gcm-open") || !strings.Contains(out.String(), "stage=decrypt-all-keys")) {
				t.Errorf("debug lines do not name the stages: %q", out.String())
			}
			if strings.Contains(out.String(), "secret plaintext") || strings.Contains(out.String(), string(key)) {
				t.Errorf("debug line leaks key or plaintext: %q", out.String())
			}
			if s := stdlib.String(); s != "" {
				t.Errorf("stdlib got %q, want nothing", s)
			}
		})
	}
}

// SetLogger may run while decrypts that write through the logger are in
// flight: the logger is held under the driver's mutex.
func TestAESDriver_SetLoggerWhileDecryptingIsSafe(t *testing.T) {
	t.Setenv("CRYPTO_DEBUG", "true")
	d, err := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")
	if err != nil {
		t.Fatalf("NewAESDriver: %v", err)
	}
	out := &fallbacklogtest.Output{}
	fallbacklogtest.Capture(t)
	legacy := handCraftLegacyCBC(t, d, []byte("old"))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				d.SetLogger(consoleTo(out))
				d.SetLogger(nil)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = d.Decrypt("not a payload")
				_, _ = d.Decrypt(legacy)
			}
		}()
	}
	wg.Wait()
}

// SetLogger's edge inputs: nil puts the driver back on the fallback, and a
// zero-value driver takes a logger and resolves nil to the fallback.
func TestAESDriver_SetLoggerEdgeInputs(t *testing.T) {
	t.Run("nil restores the fallback", func(t *testing.T) {
		fallback := fallbacklogtest.Capture(t)
		out := &fallbacklogtest.Output{}
		d, err := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")
		if err != nil {
			t.Fatalf("NewAESDriver: %v", err)
		}
		d.SetLogger(consoleTo(out))
		d.SetLogger(nil)
		if _, err := d.Decrypt(handCraftLegacyCBC(t, d, []byte("old"))); err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if out.String() != "" {
			t.Errorf("replaced logger got %q, want nothing", out.String())
		}
		if got := fallback.Count("WARN", "velocity/crypto: legacy v0 payload decrypted"); got != 1 {
			t.Errorf("fallback warn lines = %d, want 1 (%q)", got, fallback.String())
		}
	})

	t.Run("zero value", func(t *testing.T) {
		var d AESDriver
		l := consoleTo(&fallbacklogtest.Output{})
		d.SetLogger(l)
		if got := d.log(); got != l {
			t.Errorf("log() = %v, want the installed logger", got)
		}
		d.SetLogger(nil)
		if _, ok := d.log().(fallbacklog.Logger); !ok {
			t.Errorf("log() after SetLogger(nil) = %T, want fallbacklog.Logger", d.log())
		}
	})
}
