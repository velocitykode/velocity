package storage

import (
	"errors"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// Common errors. ErrFileNotFound and ErrDiskNotFound are aliases for the
// hoisted contract.ErrFileNotFound / contract.ErrDiskNotFound so callers can
// errors.Is against the shared identity without importing storage.
var (
	ErrFileNotFound  = contract.ErrFileNotFound
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

// Driver defines the storage driver interface. The canonical declaration
// lives in the stdlib-only contract leaf; this alias keeps the storage API
// byte-identical for existing callers.
type Driver = contract.StorageDriver

// DiskConfig holds configuration for a storage disk. The canonical
// declaration (with its String redaction method) lives in the contract leaf.
type DiskConfig = contract.StorageDiskConfig

// Config holds storage configuration. Canonical declaration in the contract leaf.
type Config = contract.StorageConfig

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
