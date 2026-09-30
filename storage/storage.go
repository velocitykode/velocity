package storage

import (
	"context"
	"errors"
	"sync"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/driverregistry"
	"github.com/velocitykode/velocity/internal/errchain"
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
	// shutdowns shuts the detached disks down, one run at a time (see
	// Shutdown). Detach is called under mu.
	shutdowns teardown.Children[Driver]
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
			err = errchain.Errorf("velocity/storage: failed to create driver for disk %s: %w", name, derr)
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
				errs = append(errs, errchain.Errorf("velocity/storage: shut down unpublished disk %q: %w", name, derr))
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
	return m.diskLocked(name)
}

// Default returns the default disk driver.
// Returns ErrDiskNotFound if the default disk has not been configured.
// The default's name and its disk are read under one lock, so a
// concurrent SetDefault or Configure is seen whole or not at all.
func (m *Manager) Default() (Driver, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.diskLocked(m.defaultDisk)
}

// diskLocked returns the disk name. The caller holds m.mu.
func (m *Manager) diskLocked(name string) (Driver, error) {
	if driver, ok := m.disks[name]; ok {
		return driver, nil
	}
	return nil, errchain.Errorf("velocity/storage: disk %q not found: %w", name, ErrDiskNotFound)
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
// aggregated via errors.Join so no partial failure is masked. The disk
// registry is cleared regardless of errors so a closed driver is no longer
// resolvable via Disk().
//
// The children are shut down on a goroutine of the manager's own, as one
// run; Shutdown waits for it or for ctx, whichever ends first, and at ctx
// the run goes on. A Shutdown that overlaps a run waits for the same run,
// and one that finds nothing new returns that run's retained result
// instead of nil, also when a child was published and removed again since.
// A Shutdown's result covers every child published before the call,
// including those an earlier Shutdown was still closing. A Shutdown called
// from a child's Shutdown returns an error wrapping
// contract.ErrStopFromOwnWork at once: it would wait on itself.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	children := m.disks
	m.disks = make(map[string]Driver)
	wait := m.shutdowns.Detach(children, func(name string, err error) error {
		return errchain.Errorf("velocity/storage: shutdown disk %q: %w", name, err)
	})
	m.generation++
	m.mu.Unlock()
	return wait(ctx)
}

// createDriverWithContext creates a driver using the provided context for
// drivers that require network I/O during construction (e.g. s3).
func createDriverWithContext(ctx context.Context, config DiskConfig) (Driver, error) {
	d, err := drivers.Resolve(ctx, config.Driver, config)
	if err != nil {
		return nil, errchain.Errorf("velocity/storage: %w", err)
	}
	return d, nil
}
