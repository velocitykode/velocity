package router

import (
	"net/http/httptest"
	"testing"
)

// TestContext_File_DefaultsCacheControlPrivateNoStore pins FS-CACHE-CTL
// for File(). Auth-gated downloads must default to non-cacheable so
// shared intermediaries do not store the body keyed on URL alone.
func TestContext_File_DefaultsCacheControlPrivateNoStore(t *testing.T) {
	fpath, root := chdirTempForFile(t, "cc.txt", []byte("cache me not"))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = root

	if err := c.File(fpath); err != nil {
		t.Fatalf("File: %v", err)
	}
	if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "private, no-store")
	}
}

// TestContext_File_PreservesCallerSetCacheControl pins the caller-wins
// branch: a handler that wants public caching can SetHeader before
// File() and the default must not clobber the value.
func TestContext_File_PreservesCallerSetCacheControl(t *testing.T) {
	fpath, root := chdirTempForFile(t, "cc.txt", []byte("public asset"))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = root

	c.SetHeader("Cache-Control", "public, max-age=3600")
	if err := c.File(fpath); err != nil {
		t.Fatalf("File: %v", err)
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Errorf("Cache-Control = %q, want caller-set %q", got, "public, max-age=3600")
	}
}

// TestContext_Download_DefaultsCacheControlPrivateNoStore mirrors the
// File() test for Download().
func TestContext_Download_DefaultsCacheControlPrivateNoStore(t *testing.T) {
	fpath, root := chdirTempForFile(t, "dl.txt", []byte("download me"))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = root

	if err := c.Download(fpath, "report.txt"); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "private, no-store")
	}
}

// TestContext_Download_PreservesCallerSetCacheControl pins caller-wins
// for Download().
func TestContext_Download_PreservesCallerSetCacheControl(t *testing.T) {
	fpath, root := chdirTempForFile(t, "dl.txt", []byte("cached download"))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	c := NewContext(w, req)
	c.fileRoot = root

	c.SetHeader("Cache-Control", "public, max-age=600")
	if err := c.Download(fpath, "report.txt"); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=600" {
		t.Errorf("Cache-Control = %q, want caller-set %q", got, "public, max-age=600")
	}
}
