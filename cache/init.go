package cache

import (
	"context"
	"fmt"

	"github.com/velocitykode/velocity/cache/drivers"
)

// init wires the framework's built-in cache stores into the canonical
// driver registry. Registrations happen at package import time so that
// importing cache anywhere (directly or transitively) makes the standard
// drivers available without needing a blank import. Third-party stores
// can register additional factories via cache.Drivers().Register(...).
//
// Each factory closure consumes the resolved StoreConfig (see
// Manager.createStore which merges global + store-local prefix into a
// per-call StoreConfig copy before calling Resolve).
func init() {
	Drivers().Register(DriverMemory, func(_ context.Context, cfg StoreConfig) (Store, error) {
		return drivers.NewMemoryStore(cfg.Prefix), nil
	})

	Drivers().Register(DriverFile, func(_ context.Context, cfg StoreConfig) (Store, error) {
		return drivers.NewFileStore(cfg.Prefix, cfg.Path)
	})

	Drivers().Register(DriverRedis, func(ctx context.Context, cfg StoreConfig) (Store, error) {
		return drivers.NewRedisStore(ctx, cfg.Prefix, cfg.Host, cfg.Port, cfg.Password, cfg.Database, cfg.TLS)
	})

	Drivers().Register(DriverDatabase, func(_ context.Context, _ StoreConfig) (Store, error) {
		return nil, fmt.Errorf("velocity/cache: database driver not yet implemented")
	})
}
