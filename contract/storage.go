package contract

import (
	"context"
	"fmt"
	"io"
	"time"
)

// StorageManager is the interface satisfied by the storage manager. It covers
// the methods used through app.Services and router.Context for disk management.
type StorageManager interface {
	Disk(name string) (StorageDriver, error)
	Default() (StorageDriver, error)
	AddDisk(name string, driver StorageDriver)
	SetDefault(name string) error
	Configure(config StorageConfig) error
	Shutdown(ctx context.Context) error
}

// StorageDriver defines the storage driver interface.
//
// Every method that performs I/O (local fs, network/S3, etc.) comes in
// pairs: a `Ctx`-suffixed variant that threads the caller's context.Context
// through to the underlying SDK call (so a slow S3 GET or a hung fsync can
// be cancelled when the request context is cancelled), and a non-Ctx
// Deprecated shim that calls the Ctx variant with context.Background().
// New code MUST call the Ctx variants.
//
// URL is excluded from the rule, since it is a pure string operation with
// no I/O.
//
// Implementations must pass storagetest.RunDriverContractTests. See
// storagetest for the executable specification.
type StorageDriver interface {
	// Basic operations
	PutCtx(ctx context.Context, path string, contents []byte) error
	// Deprecated: use PutCtx with a request-scoped context.Context.
	Put(path string, contents []byte) error

	PutStreamCtx(ctx context.Context, path string, stream io.Reader) error
	// Deprecated: use PutStreamCtx with a request-scoped context.Context.
	PutStream(path string, stream io.Reader) error

	GetCtx(ctx context.Context, path string) ([]byte, error)
	// Deprecated: use GetCtx with a request-scoped context.Context.
	Get(path string) ([]byte, error)

	GetStreamCtx(ctx context.Context, path string) (io.ReadCloser, error)
	// Deprecated: use GetStreamCtx with a request-scoped context.Context.
	GetStream(path string) (io.ReadCloser, error)

	// File management
	ExistsCtx(ctx context.Context, path string) bool
	// Deprecated: use ExistsCtx with a request-scoped context.Context.
	Exists(path string) bool

	DeleteCtx(ctx context.Context, paths ...string) error
	// Deprecated: use DeleteCtx with a request-scoped context.Context.
	Delete(paths ...string) error

	CopyCtx(ctx context.Context, from, to string) error
	// Deprecated: use CopyCtx with a request-scoped context.Context.
	Copy(from, to string) error

	MoveCtx(ctx context.Context, from, to string) error
	// Deprecated: use MoveCtx with a request-scoped context.Context.
	Move(from, to string) error

	// File information
	SizeCtx(ctx context.Context, path string) (int64, error)
	// Deprecated: use SizeCtx with a request-scoped context.Context.
	Size(path string) (int64, error)

	LastModifiedCtx(ctx context.Context, path string) (time.Time, error)
	// Deprecated: use LastModifiedCtx with a request-scoped context.Context.
	LastModified(path string) (time.Time, error)

	MimeTypeCtx(ctx context.Context, path string) (string, error)
	// Deprecated: use MimeTypeCtx with a request-scoped context.Context.
	MimeType(path string) (string, error)

	// Directory operations
	FilesCtx(ctx context.Context, directory string) ([]string, error)
	// Deprecated: use FilesCtx with a request-scoped context.Context.
	Files(directory string) ([]string, error)

	AllFilesCtx(ctx context.Context, directory string) ([]string, error)
	// Deprecated: use AllFilesCtx with a request-scoped context.Context.
	AllFiles(directory string) ([]string, error)

	DirectoriesCtx(ctx context.Context, directory string) ([]string, error)
	// Deprecated: use DirectoriesCtx with a request-scoped context.Context.
	Directories(directory string) ([]string, error)

	AllDirectoriesCtx(ctx context.Context, directory string) ([]string, error)
	// Deprecated: use AllDirectoriesCtx with a request-scoped context.Context.
	AllDirectories(directory string) ([]string, error)

	MakeDirectoryCtx(ctx context.Context, path string) error
	// Deprecated: use MakeDirectoryCtx with a request-scoped context.Context.
	MakeDirectory(path string) error

	DeleteDirectoryCtx(ctx context.Context, directory string) error
	// Deprecated: use DeleteDirectoryCtx with a request-scoped context.Context.
	DeleteDirectory(directory string) error

	// URL is a pure string transformation, so no Ctx variant.
	URL(path string) string

	TemporaryURLCtx(ctx context.Context, path string, expiration time.Duration) (string, error)
	// Deprecated: use TemporaryURLCtx with a request-scoped context.Context.
	TemporaryURL(path string, expiration time.Duration) (string, error)
}

// StorageDiskConfig holds configuration for a storage disk.
// The Key and Secret fields contain sensitive credentials and must not be logged.
type StorageDiskConfig struct {
	Driver string // "local", "s3", "memory"

	// Local driver config
	Root       string // Root directory for local storage
	URL        string // Base URL for file access
	Visibility string // Default visibility (public/private)

	// S3 driver config
	Key    string // AWS Access Key, SENSITIVE: do not log
	Secret string // AWS Secret Key, SENSITIVE: do not log
	Region string // AWS Region
	Bucket string // S3 Bucket name

	// Memory driver config
	MaxSize int64 // Maximum memory usage in bytes
}

// String returns a safe representation with credentials redacted.
func (c StorageDiskConfig) String() string {
	return fmt.Sprintf("DiskConfig{Driver:%s, Region:%s, Bucket:%s, Key:[REDACTED], Secret:[REDACTED]}", c.Driver, c.Region, c.Bucket)
}

// StorageConfig holds storage configuration.
type StorageConfig struct {
	Default string                       // Default disk name
	Disks   map[string]StorageDiskConfig // Configured disks
}
