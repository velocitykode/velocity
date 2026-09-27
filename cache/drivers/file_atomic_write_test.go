package drivers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A reader on one FileStore never misses a key that is always present
// while another FileStore over the same directory (another process
// sharing the cache path) rewrites it: every write replaces the file whole,
// so a read sees the old item or the new one, never a partial file.
func TestFileStore_ConcurrentReaderNeverSeesAPartialWrite(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "shared-cache")
	writer, err := NewFileStoreWithOptions("atomic", dir, time.Hour)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	defer func() { _ = writer.Shutdown(context.Background()) }()
	reader, err := NewFileStoreWithOptions("atomic", dir, time.Hour)
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer func() { _ = reader.Shutdown(context.Background()) }()

	ctx := context.Background()
	values := []string{strings.Repeat("a", 512*1024), strings.Repeat("b", 512*1024)}
	if err := writer.PutCtx(ctx, "always", values[0], time.Hour); err != nil {
		t.Fatalf("PutCtx: %v", err)
	}
	if err := writer.SetAddCtx(ctx, "members", time.Hour, "m0"); err != nil {
		t.Fatalf("SetAddCtx: %v", err)
	}

	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; !stop.Load(); i++ {
			v := values[i%2]
			var err error
			switch i % 4 {
			case 0:
				err = writer.PutCtx(ctx, "always", v, time.Hour)
			case 1:
				err = writer.ForeverCtx(ctx, "always", v)
			case 2:
				var cur interface{}
				cur, _ = writer.GetCtx(ctx, "always")
				_, err = writer.CompareAndSwapCtx(ctx, "always", cur, v, time.Hour)
			case 3:
				err = writer.SetAddCtx(ctx, "members", time.Hour, "m1")
				if err == nil {
					err = writer.SetRemoveCtx(ctx, "members", "m1")
				}
			}
			if err != nil {
				t.Errorf("write %d: %v", i, err)
				return
			}
		}
	}()

	deadline := time.Now().Add(1500 * time.Millisecond)
	reads := 0
	for time.Now().Before(deadline) {
		v, found := reader.GetCtx(ctx, "always")
		if !found {
			stop.Store(true)
			wg.Wait()
			t.Fatalf("read %d missed a key that is always present", reads)
		}
		if s, _ := v.(string); s != values[0] && s != values[1] {
			stop.Store(true)
			wg.Wait()
			t.Fatalf("read %d returned a value no write stored (%d bytes)", reads, len(s))
		}
		if _, found := reader.GetCtx(ctx, "members"); !found {
			stop.Store(true)
			wg.Wait()
			t.Fatalf("read %d missed a set that is always present", reads)
		}
		reads++
	}
	stop.Store(true)
	wg.Wait()
	if reads == 0 {
		t.Fatal("no reads ran")
	}
}

// A temp file a write left behind is never read as an entry, and is only
// swept once it is older than the unreadable grace, so a live write's temp
// file is never removed under it.
func TestFileStore_TempFilesAreNotEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewFileStoreWithOptions("tmp", dir, time.Hour)
	if err != nil {
		t.Fatalf("NewFileStoreWithOptions: %v", err)
	}
	defer func() { _ = s.Shutdown(context.Background()) }()
	ctx := context.Background()
	if err := s.PutCtx(ctx, "k", "v", time.Hour); err != nil {
		t.Fatalf("PutCtx: %v", err)
	}
	final := s.getCacheFilePath("k")
	// An expired item under a temp name, as a write that crashed between
	// its write and its rename would leave.
	expired := []byte(`{"value":"InYi","expiration":"2000-01-01T00:00:00Z"}`)
	young := final + fileTempMarker + "young"
	stale := final + fileTempMarker + "stale"
	for _, p := range []string{young, stale} {
		if err := os.WriteFile(p, expired, cacheFileMode); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * fileUnreadableGrace)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	s.sweepExpired()
	if _, err := os.Stat(young); err != nil {
		t.Fatalf("the sweep removed a temp file inside the grace: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the sweep kept a temp file past the grace: %v", err)
	}
	if v, found := s.GetCtx(ctx, "k"); !found || v != "v" {
		t.Fatalf("GetCtx = (%v, %v), want the entry", v, found)
	}

	if err := s.FlushCtx(ctx); err != nil {
		t.Fatalf("FlushCtx: %v", err)
	}
	if _, err := os.Stat(young); err != nil {
		t.Fatalf("Flush removed a temp file inside the grace: %v", err)
	}
	if _, found := s.GetCtx(ctx, "k"); found {
		t.Fatal("Flush left the entry")
	}
}
