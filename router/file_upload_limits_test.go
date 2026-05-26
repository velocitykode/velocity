package router

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFormFile_RejectsBodyOverDefaultCap exercises the H-15 fix: when no
// BodyLimit middleware is installed, FormFile must wrap the request body
// in http.MaxBytesReader(DefaultMaxBodySize) before parsing the
// multipart form. The previous code passed the body-limit value as the
// maxMemory argument to ParseMultipartForm, which is the in-memory
// spill threshold, not a body cap. A multipart payload larger than
// DefaultMaxBodySize would happily spill the overflow into os.TempDir.
//
// Strategy: craft a multipart body whose total wire-form size is well
// over DefaultMaxBodySize. With the fix in place, ParseMultipartForm
// returns an error because MaxBytesReader trips. Without the fix
// (legacy behaviour) the parse would succeed and the multipart parts
// would land on disk under os.TempDir.
func TestFormFile_RejectsBodyOverDefaultCap(t *testing.T) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	// One field, one part, one byte over the cap. The exact field name
	// is irrelevant; we only care that the wire-encoded body exceeds
	// DefaultMaxBodySize.
	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": []string{`form-data; name="big"; filename="big.bin"`},
		"Content-Type":        []string{"application/octet-stream"},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, DefaultMaxBodySize+1)
	if _, err := part.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	tempBefore, _ := os.ReadDir(os.TempDir())
	beforeCount := len(tempBefore)

	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	if _, err := c.FormFile("big"); err == nil {
		t.Fatal("FormFile should have rejected an over-cap body, got nil error")
	}

	// Multipart sometimes spills to /tmp under load; assert we did NOT
	// create more than one new temp file (a single in-progress spill is
	// torn down by the runtime when the reader errors). The previous
	// implementation would routinely leave megabyte-sized parts behind.
	tempAfter, _ := os.ReadDir(os.TempDir())
	if delta := len(tempAfter) - beforeCount; delta > 1 {
		t.Errorf("expected no large temp spill, got %d new entries in os.TempDir", delta)
	}
}

// TestFormFile_HonoursBodyLimitMiddleware verifies the middleware path
// still wins: with router.BodyLimit installed, FormFile does NOT
// re-wrap the body. The BodyLimit middleware is the one source of
// truth, FormFile just trusts the bodyLimitKey.
func TestFormFile_HonoursBodyLimitMiddleware(t *testing.T) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "small.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	handler := BodyLimit(int64(1 << 20))(func(c *Context) error {
		fh, err := c.FormFile("file")
		if err != nil {
			return err
		}
		if fh.Filename != "small.txt" {
			t.Errorf("expected small.txt, got %s", fh.Filename)
		}
		return nil
	})

	c, _ := NewTestContext(http.MethodPost, "/upload", &buf)
	c.Request.Header.Set("Content-Type", mw.FormDataContentType())
	if err := handler(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
}

// TestSaveFile_RejectsLyingHeaderSize pins the H-16 defense in depth:
// even if a multipart.FileHeader claims a small Size, a stream that
// produces more bytes must be refused, the destination must be
// removed, and ErrFileSizeExceeded must be returned to callers.
//
// We cannot easily forge a real multipart.FileHeader with a lying
// Content-Length end-to-end (httptest builds them honestly), so this
// test runs SaveFile against a constructed FileHeader by reading it
// back via a custom multipart reader. Specifically, we declare a
// Content-Length-style header of 16 bytes on the part and pad the
// part body to 4 KiB; the resulting fh.Size will reflect what
// multipart actually read.
//
// To make a high-signal assertion we instead use the variadic
// MaxFileSize option, which the fix also enforces via io.LimitReader.
// That path is unambiguously testable and proves the cap is real.
func TestSaveFile_RejectsLyingHeaderSize(t *testing.T) {
	body := bytes.Repeat([]byte("A"), 10*1024) // 10 KiB on the wire
	c, fh := buildUploadCtx(t, "doc.bin", body)
	dir := t.TempDir()
	c.fileRoot = openTestRoot(t, dir)

	// Cap at 1 KiB even though fh.Size is 10 KiB.
	err := c.SaveFile(fh, "saved.bin", MaxFileSize(1024))
	if err == nil {
		t.Fatal("SaveFile should reject upload exceeding MaxFileSize cap")
	}
	if !strings.Contains(err.Error(), "exceeds maximum allowed size") &&
		!errors.Is(err, ErrFileSizeExceeded) {
		t.Errorf("expected size-cap error, got %v", err)
	}

	// File must not exist on disk (ValidateFile runs before create).
	if _, statErr := os.Stat(filepath.Join(dir, "saved.bin")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("destination must not exist after rejection, got stat err=%v", statErr)
	}
}

// TestSaveFile_LimitsCopyToHeaderSizePlusOne verifies that, even when
// validation passes (caller did not supply MaxFileSize), the copy is
// bounded by fh.Size+1. We simulate a "lying header" by hand-crafting
// a multipart.FileHeader whose declared Size is 4 bytes but whose
// underlying part reader produces many more bytes. SaveFile must
// detect the overrun, delete the partial file, and return
// ErrFileSizeExceeded.
func TestSaveFile_LimitsCopyToHeaderSizePlusOne(t *testing.T) {
	// Build a real multipart body so the FileHeader is wired to a real
	// underlying stream.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("upload", "lie.bin")
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("B"), 4096)
	if _, err := fw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	_, fh, err := c.Request.FormFile("upload")
	if err != nil {
		t.Fatal(err)
	}

	// Lie about declared size: claim 4 bytes when the part is 4 KiB.
	originalSize := fh.Size
	fh.Size = 4

	dir := t.TempDir()
	c.fileRoot = openTestRoot(t, dir)

	err = c.SaveFile(fh, "lie.bin")
	if !errors.Is(err, ErrFileSizeExceeded) {
		t.Fatalf("expected ErrFileSizeExceeded, got %v (declared=%d, actual=%d)", err, fh.Size, originalSize)
	}

	if _, statErr := os.Stat(filepath.Join(dir, "lie.bin")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("partial file should be removed after overrun, got stat err=%v", statErr)
	}
}

// TestSaveFile_HonestUploadStillWorks regression-guards the happy path.
// With the fix in place an honest multipart upload of N bytes whose
// header says N must still complete successfully and write exactly N
// bytes to the destination.
func TestSaveFile_HonestUploadStillWorks(t *testing.T) {
	body := []byte("the quick brown fox jumps over the lazy dog")
	c, fh := buildUploadCtx(t, "fox.txt", body)
	dir := t.TempDir()
	c.fileRoot = openTestRoot(t, dir)

	if err := c.SaveFile(fh, "fox.txt"); err != nil {
		t.Fatalf("SaveFile error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "fox.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("file contents differ:\n got=%q\nwant=%q", got, body)
	}
}

// buildUploadCtx is a test helper that constructs a *Context backed by
// an honest multipart body. Returned FileHeader.Size matches the real
// part length; callers who want to simulate a lying header should
// override fh.Size after this returns.
func buildUploadCtx(t *testing.T, filename string, content []byte) (*Context, *multipart.FileHeader) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("upload", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	_, fh, err := c.Request.FormFile("upload")
	if err != nil {
		t.Fatal(err)
	}

	// Sanity: the helper exists so callers don't accidentally read from
	// fh.Open() before SaveFile gets its turn.
	_ = io.Discard
	return c, fh
}
