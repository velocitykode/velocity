package storage

import (
	"context"
	"fmt"
	"sync"
)

// StorageManager is the interface satisfied by *Manager. It covers the
// methods used through app.Services and router.Context for disk management.
type StorageManager interface {
	Disk(name string) (Driver, error)
	Default() (Driver, error)
	AddDisk(name string, driver Driver)
	SetDefault(name string) error
	Configure(config Config) error
	Shutdown(ctx context.Context) error
}

// Verify *Manager implements StorageManager at compile time.
var _ StorageManager = (*Manager)(nil)

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

// Shutdown is a no-op for the storage manager; individual disk drivers do not
// hold long-lived connections that need draining.
func (m *Manager) Shutdown(ctx context.Context) error {
	return nil
}

// createDriver creates a driver based on configuration using context.Background().
func createDriver(config DiskConfig) (Driver, error) {
	return createDriverWithContext(context.Background(), config)
}

// createDriverWithContext creates a driver using the provided context for
// drivers that require network I/O during construction (e.g. s3).
func createDriverWithContext(ctx context.Context, config DiskConfig) (Driver, error) {
	switch config.Driver {
	case "local":
		return NewLocalDriver(config), nil
	case "s3":
		return NewS3DriverWithContext(ctx, config)
	case "memory":
		return NewMemoryDriver(config), nil
	default:
		return nil, fmt.Errorf("velocity/storage: unknown driver: %s", config.Driver)
	}
}
