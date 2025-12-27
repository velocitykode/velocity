package storage

import (
	"errors"
	"io"
	"time"
)

// Common errors
var (
	ErrFileNotFound  = errors.New("file not found")
	ErrDiskNotFound  = errors.New("disk not found")
	ErrInvalidPath   = errors.New("invalid file path")
	ErrQuotaExceeded = errors.New("storage quota exceeded")
	ErrAccessDenied  = errors.New("access denied")
	ErrNotSupported  = errors.New("operation not supported by this driver")
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

// DiskConfig holds configuration for a storage disk
type DiskConfig struct {
	Driver string // "local", "s3", "memory"

	// Local driver config
	Root       string // Root directory for local storage
	URL        string // Base URL for file access
	Visibility string // Default visibility (public/private)

	// S3 driver config
	Key    string // AWS Access Key
	Secret string // AWS Secret Key
	Region string // AWS Region
	Bucket string // S3 Bucket name

	// Memory driver config
	MaxSize int64 // Maximum memory usage in bytes
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