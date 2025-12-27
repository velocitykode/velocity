package storage

import (
	"fmt"
	"io"
	"sync"
	"time"
)

var (
	globalManager *Manager
	mu            sync.RWMutex
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

// Global API functions

// Configure configures the global storage manager
func Configure(config Config) error {
	mu.Lock()
	defer mu.Unlock()

	manager := NewManager(config)
	if err := manager.Configure(config); err != nil {
		return err
	}

	globalManager = manager
	return nil
}

// Disk returns a specific disk from the global manager
func Disk(name string) Driver {
	mu.RLock()
	defer mu.RUnlock()

	if globalManager == nil {
		return nil
	}

	return globalManager.Disk(name)
}

// getDefaultDriver returns the default driver from the global manager
func getDefaultDriver() Driver {
	mu.RLock()
	defer mu.RUnlock()

	if globalManager == nil {
		return nil
	}

	return globalManager.Default()
}

// Put stores content at the given path using the default disk
func Put(path string, contents []byte) error {
	driver := getDefaultDriver()
	if driver == nil {
		return ErrDiskNotFound
	}
	return driver.Put(path, contents)
}

// PutStream stores a stream at the given path using the default disk
func PutStream(path string, stream io.Reader) error {
	driver := getDefaultDriver()
	if driver == nil {
		return ErrDiskNotFound
	}
	return driver.PutStream(path, stream)
}

// Get retrieves content from the given path using the default disk
func Get(path string) ([]byte, error) {
	driver := getDefaultDriver()
	if driver == nil {
		return nil, ErrDiskNotFound
	}
	return driver.Get(path)
}

// GetStream retrieves a stream from the given path using the default disk
func GetStream(path string) (io.ReadCloser, error) {
	driver := getDefaultDriver()
	if driver == nil {
		return nil, ErrDiskNotFound
	}
	return driver.GetStream(path)
}

// Exists checks if a file exists at the given path using the default disk
func Exists(path string) bool {
	driver := getDefaultDriver()
	if driver == nil {
		return false
	}
	return driver.Exists(path)
}

// Delete removes files at the given paths using the default disk
func Delete(paths ...string) error {
	driver := getDefaultDriver()
	if driver == nil {
		return ErrDiskNotFound
	}
	return driver.Delete(paths...)
}

// Copy copies a file from one path to another using the default disk
func Copy(from, to string) error {
	driver := getDefaultDriver()
	if driver == nil {
		return ErrDiskNotFound
	}
	return driver.Copy(from, to)
}

// Move moves a file from one path to another using the default disk
func Move(from, to string) error {
	driver := getDefaultDriver()
	if driver == nil {
		return ErrDiskNotFound
	}
	return driver.Move(from, to)
}

// Size returns the size of a file at the given path using the default disk
func Size(path string) (int64, error) {
	driver := getDefaultDriver()
	if driver == nil {
		return 0, ErrDiskNotFound
	}
	return driver.Size(path)
}

// LastModified returns the last modified time of a file using the default disk
func LastModified(path string) (time.Time, error) {
	driver := getDefaultDriver()
	if driver == nil {
		return time.Time{}, ErrDiskNotFound
	}
	return driver.LastModified(path)
}

// MimeType returns the MIME type of a file using the default disk
func MimeType(path string) (string, error) {
	driver := getDefaultDriver()
	if driver == nil {
		return "", ErrDiskNotFound
	}
	return driver.MimeType(path)
}

// Files lists files in a directory using the default disk
func Files(directory string) ([]string, error) {
	driver := getDefaultDriver()
	if driver == nil {
		return nil, ErrDiskNotFound
	}
	return driver.Files(directory)
}

// AllFiles lists all files recursively in a directory using the default disk
func AllFiles(directory string) ([]string, error) {
	driver := getDefaultDriver()
	if driver == nil {
		return nil, ErrDiskNotFound
	}
	return driver.AllFiles(directory)
}

// Directories lists directories using the default disk
func Directories(directory string) ([]string, error) {
	driver := getDefaultDriver()
	if driver == nil {
		return nil, ErrDiskNotFound
	}
	return driver.Directories(directory)
}

// AllDirectories lists all directories recursively using the default disk
func AllDirectories(directory string) ([]string, error) {
	driver := getDefaultDriver()
	if driver == nil {
		return nil, ErrDiskNotFound
	}
	return driver.AllDirectories(directory)
}

// MakeDirectory creates a directory using the default disk
func MakeDirectory(path string) error {
	driver := getDefaultDriver()
	if driver == nil {
		return ErrDiskNotFound
	}
	return driver.MakeDirectory(path)
}

// DeleteDirectory deletes a directory using the default disk
func DeleteDirectory(directory string) error {
	driver := getDefaultDriver()
	if driver == nil {
		return ErrDiskNotFound
	}
	return driver.DeleteDirectory(directory)
}

// URL returns the public URL for a file using the default disk
func URL(path string) string {
	driver := getDefaultDriver()
	if driver == nil {
		return ""
	}
	return driver.URL(path)
}

// TemporaryURL returns a temporary URL for a file using the default disk
func TemporaryURL(path string, expiration time.Duration) (string, error) {
	driver := getDefaultDriver()
	if driver == nil {
		return "", ErrDiskNotFound
	}
	return driver.TemporaryURL(path, expiration)
}

// SetGlobalManager sets the global storage manager (mainly for testing)
func SetGlobalManager(manager *Manager) {
	mu.Lock()
	defer mu.Unlock()
	globalManager = manager
}