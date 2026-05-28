package storage

import (
	"context"
	"io"
	"time"
)

// This file holds the Ctx-suffixed driver methods for MemoryDriver. The
// in-memory store performs no I/O; the Ctx methods exist for interface
// uniformity with LocalDriver/S3Driver and honour pre-flight cancellation
// so cancelled requests do not write through to the in-memory map.

// PutCtx is the ctx-aware variant of Put.
func (d *MemoryDriver) PutCtx(ctx context.Context, path string, contents []byte) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return d.Put(path, contents)
}

// PutStreamCtx is the ctx-aware variant of PutStream.
func (d *MemoryDriver) PutStreamCtx(ctx context.Context, path string, stream io.Reader) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return d.PutStream(path, stream)
}

// GetCtx is the ctx-aware variant of Get.
func (d *MemoryDriver) GetCtx(ctx context.Context, path string) ([]byte, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	return d.Get(path)
}

// GetStreamCtx is the ctx-aware variant of GetStream.
func (d *MemoryDriver) GetStreamCtx(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	return d.GetStream(path)
}

// ExistsCtx is the ctx-aware variant of Exists.
func (d *MemoryDriver) ExistsCtx(ctx context.Context, path string) bool {
	if err := checkCtx(ctx); err != nil {
		return false
	}
	return d.Exists(path)
}

// DeleteCtx is the ctx-aware variant of Delete.
func (d *MemoryDriver) DeleteCtx(ctx context.Context, paths ...string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return d.Delete(paths...)
}

// CopyCtx is the ctx-aware variant of Copy.
func (d *MemoryDriver) CopyCtx(ctx context.Context, from, to string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return d.Copy(from, to)
}

// MoveCtx is the ctx-aware variant of Move.
func (d *MemoryDriver) MoveCtx(ctx context.Context, from, to string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return d.Move(from, to)
}

// SizeCtx is the ctx-aware variant of Size.
func (d *MemoryDriver) SizeCtx(ctx context.Context, path string) (int64, error) {
	if err := checkCtx(ctx); err != nil {
		return 0, err
	}
	return d.Size(path)
}

// LastModifiedCtx is the ctx-aware variant of LastModified.
func (d *MemoryDriver) LastModifiedCtx(ctx context.Context, path string) (time.Time, error) {
	if err := checkCtx(ctx); err != nil {
		return time.Time{}, err
	}
	return d.LastModified(path)
}

// MimeTypeCtx is the ctx-aware variant of MimeType.
func (d *MemoryDriver) MimeTypeCtx(ctx context.Context, path string) (string, error) {
	if err := checkCtx(ctx); err != nil {
		return "", err
	}
	return d.MimeType(path)
}

// FilesCtx is the ctx-aware variant of Files.
func (d *MemoryDriver) FilesCtx(ctx context.Context, directory string) ([]string, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	return d.Files(directory)
}

// AllFilesCtx is the ctx-aware variant of AllFiles.
func (d *MemoryDriver) AllFilesCtx(ctx context.Context, directory string) ([]string, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	return d.AllFiles(directory)
}

// DirectoriesCtx is the ctx-aware variant of Directories.
func (d *MemoryDriver) DirectoriesCtx(ctx context.Context, directory string) ([]string, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	return d.Directories(directory)
}

// AllDirectoriesCtx is the ctx-aware variant of AllDirectories.
func (d *MemoryDriver) AllDirectoriesCtx(ctx context.Context, directory string) ([]string, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	return d.AllDirectories(directory)
}

// MakeDirectoryCtx is the ctx-aware variant of MakeDirectory.
func (d *MemoryDriver) MakeDirectoryCtx(ctx context.Context, path string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return d.MakeDirectory(path)
}

// DeleteDirectoryCtx is the ctx-aware variant of DeleteDirectory.
func (d *MemoryDriver) DeleteDirectoryCtx(ctx context.Context, directory string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return d.DeleteDirectory(directory)
}

// TemporaryURLCtx is the ctx-aware variant of TemporaryURL.
func (d *MemoryDriver) TemporaryURLCtx(ctx context.Context, path string, expiration time.Duration) (string, error) {
	if err := checkCtx(ctx); err != nil {
		return "", err
	}
	return d.TemporaryURL(path, expiration)
}
