package storage

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// errReader returns an error after yielding nothing, simulating a stream
// that fails partway through being read.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

// TestMemoryDriver_PutStreamRejectsTraversal pins that PutStream validates
// the path before consuming the stream, so a traversal path is rejected with
// a traversal error even when the stream itself would error or exceed the
// size limit.
func TestMemoryDriver_PutStreamRejectsTraversal(t *testing.T) {
	wantTraversal := func(t *testing.T, p string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("PutStream(%q) should reject traversal path", p)
		} else if !strings.Contains(err.Error(), "traversal") {
			t.Errorf("PutStream(%q) error = %v, want traversal error", p, err)
		}
	}

	for _, p := range []string{"a/../b", "../x"} {
		t.Run(p+"/error-reader", func(t *testing.T) {
			d := NewMemoryDriver(DiskConfig{Driver: "memory"})
			wantTraversal(t, p, d.PutStream(p, errReader{}))
		})

		t.Run(p+"/over-limit", func(t *testing.T) {
			d := NewMemoryDriver(DiskConfig{Driver: "memory", MaxSize: 4})
			oversize := io.LimitReader(neverEnding{}, 1024)
			wantTraversal(t, p, d.PutStream(p, oversize))
		})
	}
}

// neverEnding yields an endless stream of zero bytes.
type neverEnding struct{}

func (neverEnding) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = 0
	}
	return len(b), nil
}

// TestMemoryDriver_RejectsTraversal pins the path-policy alignment fix:
// the in-memory driver must reject ".." segments exactly like the s3
// driver, so a key that is accepted in tests against memory cannot
// explode (or worse, escape) when the same code runs against a real
// filesystem- or object-backed disk.
func TestMemoryDriver_RejectsTraversal(t *testing.T) {
	traversalPaths := []string{"a/../b", "../x"}

	for _, p := range traversalPaths {
		t.Run(p, func(t *testing.T) {
			d := NewMemoryDriver(DiskConfig{Driver: "memory"})

			if err := d.Put(p, []byte("evil")); err == nil {
				t.Errorf("Put(%q) should reject traversal path", p)
			} else if !strings.Contains(err.Error(), "traversal") {
				t.Errorf("Put(%q) error = %v, want traversal error", p, err)
			}

			if _, err := d.Get(p); err == nil {
				t.Errorf("Get(%q) should reject traversal path", p)
			} else if !strings.Contains(err.Error(), "traversal") {
				t.Errorf("Get(%q) error = %v, want traversal error", p, err)
			}

			if err := d.Delete(p); err == nil {
				t.Errorf("Delete(%q) should reject traversal path", p)
			} else if !strings.Contains(err.Error(), "traversal") {
				t.Errorf("Delete(%q) error = %v, want traversal error", p, err)
			}

			if err := d.MakeDirectory(p); err == nil {
				t.Errorf("MakeDirectory(%q) should reject traversal path", p)
			} else if !strings.Contains(err.Error(), "traversal") {
				t.Errorf("MakeDirectory(%q) error = %v, want traversal error", p, err)
			}

			if _, err := d.TemporaryURL(p, 0); err == nil {
				t.Errorf("TemporaryURL(%q) should reject traversal path", p)
			} else if !strings.Contains(err.Error(), "traversal") {
				t.Errorf("TemporaryURL(%q) error = %v, want traversal error", p, err)
			}
		})
	}
}
