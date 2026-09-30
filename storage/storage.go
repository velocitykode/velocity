package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/driverregistry"
	"github.com/velocitykode/velocity/internal/teardown"
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
	// generation counts Shutdowns, so a Configure whose drivers are built
	// across one publishes nothing into the emptied manager. Guarded by mu.
	generation uint64
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
//
// The disks' drivers are built with no lock held: a registered driver
// factory is user code, and one that looks up a disk on this manager must
// not wait on it. The configuration and the drivers built are then
// published under one lock. When a factory fails, or panics (the panic is
// its error), the drivers built before it are still published and the
// error is returned, as before.
//
// A Configure whose drivers are built across a Shutdown publishes nothing:
// it shuts down every driver it built, writes neither the configuration
// nor the default disk, and returns an error that holds those drivers'
// Shutdown errors.
func (m *Manager) ConfigureWithContext(ctx context.Context, config Config) error {
	m.mu.RLock()
	generation := m.generation
	m.mu.RUnlock()

	built := make(map[string]Driver, len(config.Disks))
	var err error
	for name, diskConfig := range config.Disks {
		var driver Driver
		derr := teardown.Step(func() error {
			var cerr error
			driver, cerr = createDriverWithContext(ctx, diskConfig)
			return cerr
		})
		if derr != nil {
			err = fmt.Errorf("velocity/storage: failed to create driver for disk %s: %w", name, derr)
			break
		}
		built[name] = driver
	}

	m.mu.Lock()
	if m.generation != generation {
		m.mu.Unlock()
		errs := []error{errors.New("velocity/storage: the manager was shut down while the disks were configured")}
		for name, driver := range built {
			if derr := teardown.Close(ctx, driver); derr != nil {
				errs = append(errs, fmt.Errorf("velocity/storage: shut down unpublished disk %q: %w", name, derr))
			}
		}
		if err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
	defer m.mu.Unlock()
	m.config = config
	m.defaultDisk = config.Default
	for name, driver := range built {
		m.disks[name] = driver
	}
	return err
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
// skipped. Every driver's Shutdown is attempted even if an earlier one
// fails or panics (a panic is that driver's error), and the errors are
// aggregated via errors.Join so no partial failure is masked. The disk registry is cleared regardless of errors
// so a closed driver is no longer resolvable via Disk() and a second
// call is a no-op returning nil.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	disks := m.disks
	m.disks = make(map[string]Driver)
	m.generation++
	m.mu.Unlock()

	var errs []error
	for name, driver := range disks {
		if err := teardown.Close(ctx, driver); err != nil {
			errs = append(errs, fmt.Errorf("velocity/storage: shutdown disk %q: %w", name, err))
		}
	}
	return errors.Join(errs...)
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
