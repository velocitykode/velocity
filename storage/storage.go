package storage

import (
	"fmt"
	"sync"
)

// StorageManager is the interface satisfied by *Manager. It covers the
// methods used through app.Services and router.Context for disk management.
type StorageManager interface {
	Disk(name string) Driver
	Default() Driver
	AddDisk(name string, driver Driver)
	SetDefault(name string) error
	Configure(config Config) error
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
	m.mu.Lock()
	defer m.mu.Unlock()

	m.config = config
	m.defaultDisk = config.Default

	// Initialize all configured disks
	for name, diskConfig := range config.Disks {
		driver, err := createDriver(diskConfig)
		if err != nil {
			return fmt.Errorf("failed to create driver for disk %s: %w", name, err)
		}
		m.disks[name] = driver
	}

	return nil
}

// Disk returns a specific disk driver
func (m *Manager) Disk(name string) Driver {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if driver, ok := m.disks[name]; ok {
		return driver
	}

	return nil
}

// Default returns the default disk driver
func (m *Manager) Default() Driver {
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

// createDriver creates a driver based on configuration
func createDriver(config DiskConfig) (Driver, error) {
	switch config.Driver {
	case "local":
		return NewLocalDriver(config), nil
	case "s3":
		return NewS3Driver(config)
	case "memory":
		return NewMemoryDriver(config), nil
	default:
		return nil, fmt.Errorf("unknown driver: %s", config.Driver)
	}
}
