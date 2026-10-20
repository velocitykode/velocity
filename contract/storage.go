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
	// Bucket is the S3 bucket name, used by the s3 driver, which requires
	// it. velocity.ConfigFromEnv configures the "s3" disk only when it is
	// set (AWS_BUCKET, default empty).
	Bucket string

	// Driver is the disk driver: "local", "s3" or "memory".
	// velocity.ConfigFromEnv sets "local" on the "local" disk and "s3" on
	// the "s3" disk.
	Driver string

	// Key is the AWS access key, used by the s3 driver together with
	// Secret. SENSITIVE: do not log (AWS_ACCESS_KEY_ID, default empty).
	Key string

	// MaxSize is a size limit in bytes: the memory driver's maximum memory
	// usage (0 means 100 MiB) and the local driver's maximum file size
	// (0 or less means 100 MiB).
	MaxSize int64

	// Region is the AWS region, used by the s3 driver, which requires it
	// (AWS_DEFAULT_REGION, default empty).
	Region string

	// Root is the root directory for local storage, used by the local
	// driver (FILESYSTEM_LOCAL_ROOT, default "./storage/app").
	Root string

	// Secret is the AWS secret key, used by the s3 driver together with
	// Key. SENSITIVE: do not log (AWS_SECRET_ACCESS_KEY, default empty).
	Secret string

	// URL is the base URL for file access, used by the local and s3
	// drivers. velocity.ConfigFromEnv sets it on the "s3" disk only (AWS_URL,
	// default empty).
	URL string

	// Visibility is the default visibility, "public" or "private", used
	// by the local and s3 drivers. Anything other than "public" is
	// private.
	Visibility string
}

// String returns a safe representation with credentials redacted.
func (c StorageDiskConfig) String() string {
	return fmt.Sprintf("DiskConfig{Driver:%s, Region:%s, Bucket:%s, Key:[REDACTED], Secret:[REDACTED]}", c.Driver, c.Region, c.Bucket)
}

// StorageConfig holds storage configuration.
type StorageConfig struct {
	// Default is the default disk name. When set and Disks is not empty,
	// Validate requires it to name one of Disks (STORAGE_DRIVER, default
	// "local").
	Default string

	// Disks maps a disk name to its configuration. velocity.ConfigFromEnv always
	// configures "local", and "s3" when AWS_BUCKET is set.
	Disks map[string]StorageDiskConfig
}

// Validate checks the StorageConfig for STRUCTURAL problems only. Each disk
// must name a non-empty driver and satisfy that driver's required fields (s3
// needs a bucket), and the default disk (if set) must exist in Disks.
//
// The driver name is not checked against an allowlist: an unknown or unwired
// disk driver surfaces as a typed NotFoundError when the storage driver
// registry resolves it.
func (c StorageConfig) Validate() error {
	for name, disk := range c.Disks {
		if disk.Driver == "" {
			return fmt.Errorf("velocity/storage: disk %q has empty driver", name)
		}
		if disk.Driver == "s3" && disk.Bucket == "" {
			return fmt.Errorf("velocity/storage: disk %q uses the s3 driver but Bucket is empty", name)
		}
	}
	if c.Default != "" && len(c.Disks) > 0 {
		if _, ok := c.Disks[c.Default]; !ok {
			return fmt.Errorf("velocity/storage: default disk %q is not configured", c.Default)
		}
	}
	return nil
}
