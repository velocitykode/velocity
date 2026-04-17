package rules

import (
	"fmt"
	"mime/multipart"
	"path/filepath"
	"strings"
)

// FileHeader is the minimum shape we care about for file-related rules.
// In practice the concrete type is *multipart.FileHeader, but we also
// accept anything that carries a Filename string and a Size int64 — this
// lets tests pass in a lightweight fake without constructing a real
// multipart form.
type FileHeader interface {
	// Filename returns the original filename the client supplied.
	Filename() string
	// Size returns the file size in bytes.
	Size() int64
}

// fileLike extracts FileHeader-compatible metadata from common shapes.
func fileLike(v interface{}) (name string, size int64, ok bool) {
	switch f := v.(type) {
	case nil:
		return "", 0, false
	case *multipart.FileHeader:
		if f == nil {
			return "", 0, false
		}
		return f.Filename, f.Size, true
	case multipart.FileHeader:
		return f.Filename, f.Size, true
	case FileHeader:
		if f == nil {
			return "", 0, false
		}
		return f.Filename(), f.Size(), true
	}
	return "", 0, false
}

// imageExtensions lists the extensions ImageRule recognises. The check is
// filename-suffix-based — Velocity does not read file contents in the
// validation layer.
var imageExtensions = map[string]struct{}{
	".jpg":  {},
	".jpeg": {},
	".png":  {},
	".gif":  {},
	".webp": {},
	".bmp":  {},
	".svg":  {},
	".heic": {},
	".heif": {},
	".avif": {},
}

// FileRule validates that a value carries file-upload metadata
// (a *multipart.FileHeader or equivalent).
//
// Note: This rule is a shape check on the value stored in the validation
// data map. Velocity's ExtractRequestData currently does not populate
// multipart files — handlers that need file validation should merge the
// *multipart.FileHeader into the data map before calling Check.
func FileRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	if _, _, ok := fileLike(value); !ok {
		return fmt.Errorf("The %s field must be a file.", field)
	}
	return nil
}

// MimesRule validates that an uploaded file's extension matches one of
// the whitelisted short mime names. Usage: mimes:jpg,png,pdf
//
// The check is based on the filename suffix (case-insensitive, leading dot
// optional on the parameter). It does not sniff file contents.
func MimesRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	if len(params) < 1 {
		return fmt.Errorf("The mimes rule requires at least 1 parameter.")
	}
	name, _, ok := fileLike(value)
	if !ok {
		return fmt.Errorf("The %s field must be a file.", field)
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	for _, p := range params {
		if strings.TrimPrefix(strings.ToLower(strings.TrimSpace(p)), ".") == ext {
			return nil
		}
	}
	return fmt.Errorf("The %s field must be a file of type: %s.", field, strings.Join(params, ", "))
}

// ImageRule validates that an uploaded file looks like an image based on
// its extension. See MimesRule's note about filename-based detection.
func ImageRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	name, _, ok := fileLike(value)
	if !ok {
		return fmt.Errorf("The %s field must be an image.", field)
	}
	ext := strings.ToLower(filepath.Ext(name))
	if _, ok := imageExtensions[ext]; !ok {
		return fmt.Errorf("The %s field must be an image.", field)
	}
	return nil
}
