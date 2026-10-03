package storage

import (
	"context"
	"errors"
	"maps"
	"slices"
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
	// shutdowns is the disks' lifecycle: it shuts the registry down, one
	// run at a time (see Shutdown), retires a disk that leaves it
	// otherwise, and counts Shutdowns, so a Configure whose drivers are
	// built across one publishes nothing into the emptied manager. Called
	// under mu.
	shutdowns teardown.Children[Driver]
	// owners asks the disks whether they own the caller, and remembers
	// whose panic it reported. Called with mu released.
	owners teardown.Owners
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
// A disk Configure replaces is closed before it returns, as AddDisk
// closes one.
//
// A Configure whose drivers are built across a Shutdown publishes nothing:
// it shuts down every driver it built, writes neither the configuration
// nor the default disk, and returns an error that holds those drivers'
// Shutdown errors.
func (m *Manager) ConfigureWithContext(ctx context.Context, config Config) error {
	m.mu.RLock()
	generation := m.shutdowns.Generation()
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
	if m.shutdowns.Generation() != generation {
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
	m.config = config
	m.defaultDisk = config.Default
	retires := make([]retirement, 0, len(built))
	for name, driver := range built {
		old := m.disks[name]
		m.disks[name] = driver
		retires = append(retires, retirement{name, m.shutdowns.Retire(m.disks, old)})
	}
	m.mu.Unlock()
	for _, r := range retires {
		teardown.Warn(nil, "storage", r.name, r.close())
	}
	return err
}

// retirement is the close of a disk that left the registry, under its
// name.
type retirement struct {
	name  string
	close func() error
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

// AddDisk adds a new disk to the manager. It closes the disk it displaces
// before returning: contained, once, unless the manager still holds it
// under another name; the call succeeds, so a failure to close it is
// written once as a warning, through the fallback logger (the manager has
// no logger of its own). A configuration call, not concurrent with the
// disks' use.
func (m *Manager) AddDisk(name string, driver Driver) {
	m.mu.Lock()
	old := m.disks[name]
	m.disks[name] = driver
	retire := m.shutdowns.Retire(m.disks, old)
	m.mu.Unlock()
	teardown.Warn(nil, "storage", name, retire())
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
// from a child's Shutdown, or from work a disk's Shutdown waits for (the
// stream of a write the local driver is copying, also on a disk an earlier
// Shutdown is still draining), returns an error wrapping
// contract.ErrStopFromOwnWork at once and changes nothing: it would wait
// on itself.
func (m *Manager) Shutdown(ctx context.Context) error {
	if m.diskOwnsCaller() {
		return errchain.Errorf("velocity/storage: Shutdown called from work a disk's Shutdown waits for: %w", contract.ErrStopFromOwnWork)
	}
	m.mu.Lock()
	wait := m.shutdowns.Shutdown(&m.disks, func(name string, err error) error {
		return errchain.Errorf("velocity/storage: shutdown disk %q: %w", name, err)
	})
	m.mu.Unlock()
	return wait(ctx)
}

// OwnsCaller reports whether the calling goroutine is closing one of the
// manager's disks, or runs work a disk's Shutdown waits for (a write the
// local driver is copying from its stream), so a stop it calls that waits
// for the manager's Shutdown would wait on itself. A disk answers for its
// own work by exporting OwnsCaller() bool. The disks asked are the
// registered ones and the ones that left the registry and are still
// closing (a Shutdown under way drains them, or AddDisk displaced them):
// the manager's Shutdown waits for those too. They are read under the
// manager's read lock and asked after it is released. A disk's OwnsCaller
// is user code: one that panics is taken as not owning the caller, and the
// panic is written once per disk as a warning, through the fallback logger
// (the manager has no logger of its own).
func (m *Manager) OwnsCaller() bool {
	return m.shutdowns.OwnsCaller() || m.diskOwnsCaller()
}

// diskOwnsCaller reports whether a disk, registered or still closing,
// answers that the calling goroutine runs its own work.
func (m *Manager) diskOwnsCaller() bool {
	m.mu.RLock()
	disks := slices.AppendSeq(m.shutdowns.Closing(), maps.Values(m.disks))
	m.mu.RUnlock()
	for _, disk := range disks {
		if m.owners.Owns(disk) {
			return true
		}
	}
	return false
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
