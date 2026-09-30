package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// rootEntries lists every file under dir, relative and slash-separated, so
// a test can assert that a write left nothing but its object behind.
func rootEntries(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	sort.Strings(out)
	return out
}

func assertEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	got := rootEntries(t, dir)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("files under root = %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("files under root = %q, want %q", got, want)
		}
	}
}

func newTestLocalDriver(t *testing.T) (*LocalDriver, string) {
	t.Helper()
	dir := t.TempDir()
	d := NewLocalDriver(DiskConfig{Driver: "local", Root: dir})
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	return d, dir
}

// TestLocalDriver_Put_KeepsObjectNamedLikeItsTemp stores an object whose
// name is another object's name plus ".tmp": writing the other object must
// neither overwrite it nor rename it away.
func TestLocalDriver_Put_KeepsObjectNamedLikeItsTemp(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "Put"
		if stream {
			name = "PutStream"
		}
		t.Run(name, func(t *testing.T) {
			d, dir := newTestLocalDriver(t)
			if err := d.Put("a.tmp", []byte("kept")); err != nil {
				t.Fatalf("Put a.tmp: %v", err)
			}
			var err error
			if stream {
				err = d.PutStream("a", bytes.NewReader([]byte("new")))
			} else {
				err = d.Put("a", []byte("new"))
			}
			if err != nil {
				t.Fatalf("%s a: %v", name, err)
			}
			got, err := d.Get("a.tmp")
			if err != nil || string(got) != "kept" {
				t.Fatalf("Get a.tmp = %q, %v; want %q", got, err, "kept")
			}
			if got, err := d.Get("a"); err != nil || string(got) != "new" {
				t.Fatalf("Get a = %q, %v; want %q", got, err, "new")
			}
			assertEntries(t, dir, "a", "a.tmp")
		})
	}
}

// pausingReader returns first, then waits for release before returning
// rest: a writer paused in the middle of its stream.
type pausingReader struct {
	first, rest []byte
	paused      chan struct{}
	release     chan struct{}
	step        int
}

func newPausingReader(first, rest string) *pausingReader {
	return &pausingReader{first: []byte(first), rest: []byte(rest), paused: make(chan struct{}), release: make(chan struct{})}
}

func (r *pausingReader) Read(p []byte) (int, error) {
	r.step++
	switch r.step {
	case 1:
		return copy(p, r.first), nil
	case 2:
		close(r.paused)
		<-r.release
		return copy(p, r.rest), nil
	default:
		return 0, io.EOF
	}
}

// TestLocalDriver_PutStream_ConcurrentWritersKeepTheirBytes pauses one
// writer mid-stream while a second writes the same path whole. The object
// ends up holding one writer's bytes, never a mix of both.
func TestLocalDriver_PutStream_ConcurrentWritersKeepTheirBytes(t *testing.T) {
	d, dir := newTestLocalDriver(t)
	a := newPausingReader("AAAA", "aaaa")
	aDone := make(chan error, 1)
	go func() { aDone <- d.PutStream("x", a) }() //safe-goroutine: the paused writer; its result is read below
	<-a.paused

	hostile.Within(t, hostile.Deadline, func() {
		if err := d.PutStream("x", bytes.NewReader([]byte("BBBBBBBB"))); err != nil {
			t.Errorf("second PutStream: %v", err)
		}
	})
	close(a.release)
	if err := <-aDone; err != nil {
		t.Fatalf("first PutStream: %v", err)
	}

	got, err := d.Get("x")
	if err != nil {
		t.Fatalf("Get x: %v", err)
	}
	if s := string(got); s != "AAAAaaaa" && s != "BBBBBBBB" {
		t.Fatalf("x = %q, want one writer's bytes (%q or %q)", s, "AAAAaaaa", "BBBBBBBB")
	}
	assertEntries(t, dir, "x")
}

// erroringReader returns some bytes, then err.
type erroringReader struct {
	err  error
	done bool
}

func (r *erroringReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	return copy(p, "partial"), nil
}

// TestLocalDriver_PutStream_LeavesNoTempOnFailure covers every way a
// stream write can fail after its temp file exists: a reader error, a
// stream over the size limit, and a reader panic. Each leaves the root as
// it was, and the driver keeps working.
func TestLocalDriver_PutStream_LeavesNoTempOnFailure(t *testing.T) {
	readErr := errors.New("reader broke")
	cases := []struct {
		name   string
		reader func(t *testing.T) io.Reader
		check  func(t *testing.T, err error, panicked any)
	}{
		{
			name:   "reader error",
			reader: func(*testing.T) io.Reader { return &erroringReader{err: readErr} },
			check: func(t *testing.T, err error, _ any) {
				if !errors.Is(err, readErr) {
					t.Fatalf("PutStream = %v, want %v", err, readErr)
				}
			},
		},
		{
			name:   "over the size limit",
			reader: func(*testing.T) io.Reader { return bytes.NewReader(make([]byte, 64)) },
			check: func(t *testing.T, err error, _ any) {
				if !errors.Is(err, ErrQuotaExceeded) {
					t.Fatalf("PutStream = %v, want ErrQuotaExceeded", err)
				}
			},
		},
		{
			name: "reader panic",
			reader: func(t *testing.T) io.Reader {
				return &codeReader{code: hostile.New(t, hostile.Panic, nil), data: []byte("x")}
			},
			check: func(t *testing.T, _ error, panicked any) {
				if panicked != hostile.PanicValue {
					t.Fatalf("panic = %v, want the reader's panic to reach the caller", panicked)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			d := NewLocalDriver(DiskConfig{Driver: "local", Root: dir, MaxSize: 32})
			t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
			if err := d.Put("sub/keep", []byte("k")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			var err error
			panicked := hostile.Within(t, hostile.Deadline, func() {
				err = d.PutStream("sub/obj", tc.reader(t))
			})
			tc.check(t, err, panicked)
			assertEntries(t, dir, "sub/keep")
			if err := d.PutStream("sub/obj", bytes.NewReader([]byte("ok"))); err != nil {
				t.Fatalf("PutStream after the failure: %v", err)
			}
			assertEntries(t, dir, "sub/keep", "sub/obj")
		})
	}
}

// codeReader runs its hostile code on the first Read, then returns data
// and EOF.
type codeReader struct {
	code *hostile.Code
	data []byte
	read bool
}

func (r *codeReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.code.Run()
	r.read = true
	return copy(p, r.data), nil
}
