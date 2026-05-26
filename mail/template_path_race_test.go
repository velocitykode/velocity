package mail

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// TestTemplatePathConcurrentReaderWriter exercises SetTemplatePath
// against simultaneous Template() reads from many goroutines. Before
// M-21 the package-level `templatePath string` was a bare two-word
// value subject to torn reads under concurrent write; `-race` would
// flag the unsynchronised access. The atomic.Value store closes that
// gap. Run with `go test -race`.
func TestTemplatePathConcurrentReaderWriter(t *testing.T) {
	// Build two real template directories so each writer state has a
	// legitimate template to point at; we want to exercise the path
	// switching, not template-not-found errors.
	tmp := t.TempDir()
	dirA := filepath.Join(tmp, "a")
	dirB := filepath.Join(tmp, "b")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
		if err := os.WriteFile(filepath.Join(d, "t.html"), []byte("<p>{{.Name}}</p>"), 0o644); err != nil {
			t.Fatalf("write %s/t.html: %v", d, err)
		}
	}

	// Restore the original template path after the test to avoid
	// leaking state into other tests in this package.
	t.Cleanup(func() { SetTemplatePath(defaultTemplatePath) })
	SetTemplatePath(dirA)

	const (
		readers = 32
		writers = 4
		iters   = 200
	)

	var wg sync.WaitGroup
	var stop atomic.Bool

	// Writers flip the template path between two real dirs as fast
	// as they can.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			toggle := false
			for i := 0; i < iters; i++ {
				toggle = !toggle
				if toggle {
					SetTemplatePath(dirA)
				} else {
					SetTemplatePath(dirB)
				}
			}
		}(w)
	}

	// Readers hammer Template() concurrently. We do not assert on
	// the resolved body (the dir can flip between Join and read),
	// only that no read corrupts the path string in a way the race
	// detector or path validator would catch.
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if stop.Load() {
					return
				}
				_, _ = NewMessage().Template("t", map[string]string{"Name": "X"})
			}
		}()
	}

	wg.Wait()
	stop.Store(true)
}

// TestSetTemplatePathStoresValue verifies that the atomic.Value
// load/store pipeline reflects the most recent SetTemplatePath call.
func TestSetTemplatePathStoresValue(t *testing.T) {
	t.Cleanup(func() { SetTemplatePath(defaultTemplatePath) })

	SetTemplatePath("/var/foo")
	if got := templatePath(); got != "/var/foo" {
		t.Errorf("expected /var/foo, got %q", got)
	}

	SetTemplatePath("/etc/bar")
	if got := templatePath(); got != "/etc/bar" {
		t.Errorf("expected /etc/bar, got %q", got)
	}
}

// TestTemplatePathDefault verifies that the initial value matches
// defaultTemplatePath before any SetTemplatePath call. Because the
// package-level init runs once, we simulate the fresh state by
// re-storing the default and confirming the read path round-trips.
func TestTemplatePathDefault(t *testing.T) {
	t.Cleanup(func() { SetTemplatePath(defaultTemplatePath) })

	SetTemplatePath(defaultTemplatePath)
	if got := templatePath(); got != defaultTemplatePath {
		t.Errorf("expected default %q, got %q", defaultTemplatePath, got)
	}
}
