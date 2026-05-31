package storage

import (
	"context"
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/driverregistry"
)

// drivers is the canonical Velocity driver registry for storage. Disk
// drivers register themselves via Drivers().Register from an init().
var drivers = driverregistry.New[Driver, DiskConfig]("storage")

// Drivers returns the registry that storage drivers register themselves
// into. Use this from a driver package's init() to install a factory:
//
//	func init() {
//	    storage.Drivers().Register("local", func(_ context.Context, cfg storage.DiskConfig) (storage.Driver, error) {
//	        return storage.NewLocalDriver(cfg), nil
//	    })
//	}
func Drivers() *driverregistry.Registry[Driver, DiskConfig] { return drivers }

// StorageManager is the interface satisfied by *Manager. It covers the
// methods used through app.Services and router.Context for disk management.
// The canonical declaration lives in the stdlib-only contract leaf.
type StorageManager = contract.StorageManager

// Verify *Manager implements StorageManager at compile time.
var _ contract.StorageManager = (*Manager)(nil)

// Verify the in-package storage drivers satisfy the contract driver interface.
// The s3 driver lives in its own storage/s3 package and is left untouched here.
var (
	_ contract.StorageDriver = (*LocalDriver)(nil)
	_ contract.StorageDriver = (*MemoryDriver)(nil)
)

// Manager manages multiple storage disks
type Manager struct {
	mu          sync.RWMutex
	disks       map[string]Driver
	config      Config
	defaultDisk string
}

// NewManager creates a new storage manager
func NewManager(config Config) *Manager {
	return &Manager{
		disks:       make(map[string]Driver),
		config:      config,
		defaultDisk: config.Default,
	}
}

// Configure configures the storage manager with the given configuration
func (m *Manager) Configure(config Config) error {
	return m.ConfigureWithContext(context.Background(), config)
}

// ConfigureWithContext is the context-aware variant of Configure. The context
// is used when bootstrapping context-aware drivers (e.g. s3).
func (m *Manager) ConfigureWithContext(ctx context.Context, config Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.config = config
	m.defaultDisk = config.Default

	// Initialize all configured disks
	for name, diskConfig := range config.Disks {
		driver, err := createDriverWithContext(ctx, diskConfig)
		if err != nil {
			return fmt.Errorf("velocity/storage: failed to create driver for disk %s: %w", name, err)
		}
		m.disks[name] = driver
	}

	return nil
}

// Disk returns a specific disk driver.
// Returns ErrDiskNotFound if the named disk has not been configured.
func (m *Manager) Disk(name string) (Driver, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if driver, ok := m.disks[name]; ok {
		return driver, nil
	}

	return nil, fmt.Errorf("velocity/storage: disk %q not found: %w", name, ErrDiskNotFound)
}

// Default returns the default disk driver.
// Returns ErrDiskNotFound if the default disk has not been configured.
func (m *Manager) Default() (Driver, error) {
	return m.Disk(m.defaultDisk)
}

// AddDisk adds a new disk to the manager
func (m *Manager) AddDisk(name string, driver Driver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disks[name] = driver
}

// SetDefault sets the default disk
func (m *Manager) SetDefault(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.disks[name]; !ok {
		return ErrDiskNotFound
	}

	m.defaultDisk = name
	return nil
}

// Shutdown drains each configured disk driver. Drivers that implement
// contract.ShutdownAware (e.g. LocalDriver, which holds an *os.Root
// file descriptor) get their Shutdown called; drivers that don't are
// skipped. The first non-nil error is returned, but every driver's
// Shutdown is attempted first so resources never leak because of an
// earlier failure.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firstErr error
	for name, driver := range m.disks {
		sd, ok := driver.(contract.ShutdownAware)
		if !ok {
			continue
		}
		if err := sd.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("velocity/storage: shutdown disk %q: %w", name, err)
		}
	}
	return firstErr
}

// createDriverWithContext creates a driver using the provided context for
// drivers that require network I/O during construction (e.g. s3).
func createDriverWithContext(ctx context.Context, config DiskConfig) (Driver, error) {
	d, err := drivers.Resolve(ctx, config.Driver, config)
	if err != nil {
		return nil, fmt.Errorf("velocity/storage: %w", err)
	}
	return d, nil
}
