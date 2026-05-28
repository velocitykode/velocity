package storage

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// Common errors. ErrFileNotFound and ErrDiskNotFound are aliases for the
// hoisted contract.ErrStorageFileNotFound / contract.ErrDiskNotFound so
// callers can errors.Is against the shared identity without importing
// storage.
var (
	ErrFileNotFound  = contract.ErrStorageFileNotFound
	ErrDiskNotFound  = contract.ErrDiskNotFound
	ErrInvalidPath   = errors.New("velocity/storage: invalid file path")
	ErrQuotaExceeded = errors.New("velocity/storage: quota exceeded")
	ErrAccessDenied  = errors.New("velocity/storage: access denied")
	ErrNotSupported  = errors.New("velocity/storage: operation not supported by this driver")
)

// Visibility defines file visibility
type Visibility string

const (
	Public  Visibility = "public"
	Private Visibility = "private"
)

// FileInfo contains file metadata
type FileInfo struct {
	Path         string
	Size         int64
	LastModified time.Time
	MimeType     string
	Visibility   Visibility
}

// Driver defines the storage driver interface
type Driver interface {
	// Basic operations
	Put(path string, contents []byte) error
	PutStream(path string, stream io.Reader) error
	Get(path string) ([]byte, error)
	GetStream(path string) (io.ReadCloser, error)

	// File management
	Exists(path string) bool
	Delete(paths ...string) error
	Copy(from, to string) error
	Move(from, to string) error

	// File information
	Size(path string) (int64, error)
	LastModified(path string) (time.Time, error)
	MimeType(path string) (string, error)

	// Directory operations
	Files(directory string) ([]string, error)
	AllFiles(directory string) ([]string, error)
	Directories(directory string) ([]string, error)
	AllDirectories(directory string) ([]string, error)
	MakeDirectory(path string) error
	DeleteDirectory(directory string) error

	// URL operations
	URL(path string) string
	TemporaryURL(path string, expiration time.Duration) (string, error)
}

// DiskConfig holds configuration for a storage disk.
// The Key and Secret fields contain sensitive credentials and must not be logged.
type DiskConfig struct {
	Driver string // "local", "s3", "memory"

	// Local driver config
	Root       string // Root directory for local storage
	URL        string // Base URL for file access
	Visibility string // Default visibility (public/private)

	// S3 driver config
	Key    string // AWS Access Key — SENSITIVE: do not log
	Secret string // AWS Secret Key — SENSITIVE: do not log
	Region string // AWS Region
	Bucket string // S3 Bucket name

	// Memory driver config
	MaxSize int64 // Maximum memory usage in bytes
}

// String returns a safe representation with credentials redacted.
func (c DiskConfig) String() string {
	return fmt.Sprintf("DiskConfig{Driver:%s, Region:%s, Bucket:%s, Key:[REDACTED], Secret:[REDACTED]}", c.Driver, c.Region, c.Bucket)
}

// Config holds storage configuration
type Config struct {
	Default string                // Default disk name
	Disks   map[string]DiskConfig // Configured disks
}

// PutOptions contains options for Put operations
type PutOptions struct {
	Visibility Visibility
	MimeType   string
	Metadata   map[string]string
}

// Option is a function that modifies PutOptions
type Option func(*PutOptions)

// WithVisibility sets the visibility for a file
func WithVisibility(v Visibility) Option {
	return func(o *PutOptions) {
		o.Visibility = v
	}
}

// WithMimeType sets the MIME type for a file
func WithMimeType(mimeType string) Option {
	return func(o *PutOptions) {
		o.MimeType = mimeType
	}
}

// WithMetadata sets metadata for a file
func WithMetadata(metadata map[string]string) Option {
	return func(o *PutOptions) {
		o.Metadata = metadata
	}
}
