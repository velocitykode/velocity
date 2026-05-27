package router

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// setUmaskRouter is a helper that bridges the syscall.Umask for the
// router-side file-mode tests. See storage/umask_*_test.go for the
// matching helper in the storage package.
func setUmaskRouter(t *testing.T, mask int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are POSIX-only")
	}
	old := umaskSet(mask)
	t.Cleanup(func() { umaskSet(old) })
}

// TestContext_SaveFile_DefaultsTo0o600 pins FS-MODE-PUTSTREAM for the
// router-side upload path. The previous 0o644 mode left every uploaded
// body readable to all local users.
func TestContext_SaveFile_DefaultsTo0o600(t *testing.T) {
	setUmaskRouter(t, 0o022)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("upload", "secret.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte("classified"))
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	c := NewContext(w, req)

	tmp := t.TempDir()
	c.fileRoot = openTestRoot(t, tmp)

	fh, err := c.FormFile("upload")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SaveFile(fh, "saved.bin"); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}

	info, err := os.Stat(filepath.Join(tmp, "saved.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("SaveFile wrote mode %o, want 0o600", got)
	}
}
