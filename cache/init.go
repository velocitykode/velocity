package cache

import (
	"context"
	"fmt"

	"github.com/velocitykode/velocity/cache/drivers"
)

// init wires the framework's built-in light cache stores into the canonical
// driver registry. Registrations happen at package import time so that
// importing cache anywhere (directly or transitively) makes the light
// drivers available without needing a blank import. Third-party stores
// can register additional factories via cache.Drivers().Register(...).
//
// The redis store is NOT registered here. It lives in the cache/redis leaf
// package (which carries the go-redis dependency) and self-registers from
// its own init(). Blank-import github.com/velocitykode/velocity/cache/redis
// or github.com/velocitykode/velocity/cache/standard to enable it.
//
// Each factory closure consumes the resolved StoreConfig (see
// Manager.createStore which merges global + store-local prefix into a
// per-call StoreConfig copy before calling Resolve).
func init() {
	Drivers().Register(DriverMemory, func(_ context.Context, cfg StoreConfig) (Store, error) {
		return drivers.NewMemoryStore(cfg.Prefix,
			drivers.WithMaxEntries(cfg.MaxEntries),
			drivers.WithMaxValueBytes(cfg.MaxValueBytes)), nil
	})

	Drivers().Register(DriverFile, func(_ context.Context, cfg StoreConfig) (Store, error) {
		return drivers.NewFileStore(cfg.Prefix, cfg.Path,
			drivers.WithFileMaxValueBytes(cfg.MaxValueBytes))
	})

	Drivers().Register(DriverDatabase, func(_ context.Context, _ StoreConfig) (Store, error) {
		return nil, fmt.Errorf("velocity/cache: database driver not yet implemented")
	})
}
