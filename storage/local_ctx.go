package storage

import (
	"context"
	"io"
	"time"
)

// This file holds the Ctx-suffixed driver methods for LocalDriver. The local
// filesystem operations performed by LocalDriver do not natively accept a
// context (Go's standard os package does not expose ctx-aware variants for
// most file operations), so the Ctx methods do a pre-flight ctx.Err() check
// to honour caller cancellation and otherwise delegate to the non-ctx
// implementation. New callers should prefer the *Ctx variants; the non-Ctx
// methods on LocalDriver are kept for backwards compatibility and carry a
// Deprecated tag in the Driver interface definition.

// checkCtx returns ctx.Err() if ctx is non-nil and already cancelled or
// past its deadline. Used as the pre-flight gate from every *Ctx method
// so callers honour cancellation even though the underlying filesystem
// call cannot itself be cancelled mid-flight.
func checkCtx(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// PutCtx is the ctx-aware variant of Put.
func (d *LocalDriver) PutCtx(ctx context.Context, path string, contents []byte) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return d.Put(path, contents)
}

// PutStreamCtx is the ctx-aware variant of PutStream.
func (d *LocalDriver) PutStreamCtx(ctx context.Context, path string, stream io.Reader) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return d.PutStream(path, stream)
}

// GetCtx is the ctx-aware variant of Get.
func (d *LocalDriver) GetCtx(ctx context.Context, path string) ([]byte, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	return d.Get(path)
}

// GetStreamCtx is the ctx-aware variant of GetStream.
func (d *LocalDriver) GetStreamCtx(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	return d.GetStream(path)
}

// ExistsCtx is the ctx-aware variant of Exists.
func (d *LocalDriver) ExistsCtx(ctx context.Context, path string) bool {
	if err := checkCtx(ctx); err != nil {
		return false
	}
	return d.Exists(path)
}

// DeleteCtx is the ctx-aware variant of Delete.
func (d *LocalDriver) DeleteCtx(ctx context.Context, paths ...string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return d.Delete(paths...)
}

// CopyCtx is the ctx-aware variant of Copy.
func (d *LocalDriver) CopyCtx(ctx context.Context, from, to string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return d.Copy(from, to)
}

// MoveCtx is the ctx-aware variant of Move.
func (d *LocalDriver) MoveCtx(ctx context.Context, from, to string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return d.Move(from, to)
}

// SizeCtx is the ctx-aware variant of Size.
func (d *LocalDriver) SizeCtx(ctx context.Context, path string) (int64, error) {
	if err := checkCtx(ctx); err != nil {
		return 0, err
	}
	return d.Size(path)
}

// LastModifiedCtx is the ctx-aware variant of LastModified.
func (d *LocalDriver) LastModifiedCtx(ctx context.Context, path string) (time.Time, error) {
	if err := checkCtx(ctx); err != nil {
		return time.Time{}, err
	}
	return d.LastModified(path)
}

// MimeTypeCtx is the ctx-aware variant of MimeType.
func (d *LocalDriver) MimeTypeCtx(ctx context.Context, path string) (string, error) {
	if err := checkCtx(ctx); err != nil {
		return "", err
	}
	return d.MimeType(path)
}

// FilesCtx is the ctx-aware variant of Files.
func (d *LocalDriver) FilesCtx(ctx context.Context, directory string) ([]string, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	return d.Files(directory)
}

// AllFilesCtx is the ctx-aware variant of AllFiles.
func (d *LocalDriver) AllFilesCtx(ctx context.Context, directory string) ([]string, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	return d.AllFiles(directory)
}

// DirectoriesCtx is the ctx-aware variant of Directories.
func (d *LocalDriver) DirectoriesCtx(ctx context.Context, directory string) ([]string, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	return d.Directories(directory)
}

// AllDirectoriesCtx is the ctx-aware variant of AllDirectories.
func (d *LocalDriver) AllDirectoriesCtx(ctx context.Context, directory string) ([]string, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	return d.AllDirectories(directory)
}

// MakeDirectoryCtx is the ctx-aware variant of MakeDirectory.
func (d *LocalDriver) MakeDirectoryCtx(ctx context.Context, path string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return d.MakeDirectory(path)
}

// DeleteDirectoryCtx is the ctx-aware variant of DeleteDirectory.
func (d *LocalDriver) DeleteDirectoryCtx(ctx context.Context, directory string) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	return d.DeleteDirectory(directory)
}

// TemporaryURLCtx is the ctx-aware variant of TemporaryURL.
func (d *LocalDriver) TemporaryURLCtx(ctx context.Context, path string, expiration time.Duration) (string, error) {
	if err := checkCtx(ctx); err != nil {
		return "", err
	}
	return d.TemporaryURL(path, expiration)
}
