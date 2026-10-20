package cache

import "testing"

// Config.Validate rejects the structural mistakes a lazily built store
// would otherwise hit on first use.
func TestConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"zero config", Config{}, false},
		{"memory store", SingleStoreConfig("p", StoreConfig{Driver: DriverMemory}), false},
		{"file store with path", SingleStoreConfig("p", StoreConfig{Driver: DriverFile, Path: "/tmp/c"}), false},
		{"redis store", SingleStoreConfig("p", StoreConfig{Driver: DriverRedis, Host: "h", Port: 6379, Database: 1}), false},
		{"empty driver", SingleStoreConfig("p", StoreConfig{}), true},
		{"file store without path", SingleStoreConfig("p", StoreConfig{Driver: DriverFile}), true},
		{"negative port", SingleStoreConfig("p", StoreConfig{Driver: DriverRedis, Port: -1}), true},
		{"negative database", SingleStoreConfig("p", StoreConfig{Driver: DriverRedis, Database: -1}), true},
		{"stores with empty default", Config{Stores: map[string]StoreConfig{"a": {Driver: DriverMemory}}}, true},
		{"default names a missing store", Config{Default: "b", Stores: map[string]StoreConfig{"a": {Driver: DriverMemory}}}, true},
		{"default names a store", Config{Default: "a", Stores: map[string]StoreConfig{"a": {Driver: DriverMemory}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// SingleStoreConfig is one store named "default" under the given prefix.
func TestSingleStoreConfig(t *testing.T) {
	store := StoreConfig{Driver: DriverFile, Path: "/tmp/c", MaxValueBytes: 9}
	cfg := SingleStoreConfig("pre", store)
	if cfg.Default != "default" || cfg.Prefix != "pre" {
		t.Fatalf("Default=%q Prefix=%q, want default and pre", cfg.Default, cfg.Prefix)
	}
	if len(cfg.Stores) != 1 {
		t.Fatalf("len(Stores) = %d, want 1", len(cfg.Stores))
	}
	if got := cfg.Stores["default"]; got != store {
		t.Fatalf("Stores[default] = %+v, want %+v", got, store)
	}
	if got := cfg.DefaultDriver(); got != DriverFile {
		t.Fatalf("DefaultDriver = %q, want file", got)
	}
}

// DefaultDriver is "" when the default store is not configured.
func TestDefaultDriver_Unconfigured(t *testing.T) {
	if got := (Config{}).DefaultDriver(); got != "" {
		t.Fatalf("zero config DefaultDriver = %q, want empty", got)
	}
	cfg := Config{Default: "b", Stores: map[string]StoreConfig{"a": {Driver: DriverMemory}}}
	if got := cfg.DefaultDriver(); got != "" {
		t.Fatalf("missing default DefaultDriver = %q, want empty", got)
	}
}
