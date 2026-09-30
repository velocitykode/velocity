//go:build unix

package drivers

import (
	"context"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// TestFileStore_Write_ValueMarshalerIsUserCode writes, through each plain
// write, a value whose MarshalJSON is each kind of hostile user code. The
// value is encoded before the key lock and the store mutex are taken: a
// MarshalJSON reading or writing the same store returns, a blocked one
// holds up no other call on the store, and a panicking one reaches the
// caller and leaves the store usable with the key unchanged.
func TestFileStore_Write_ValueMarshalerIsUserCode(t *testing.T) {
	ctx := context.Background()
	writes := []struct {
		name  string
		prior bool // whether k holds "v0" before the write (Add needs it absent)
		write func(s *FileStore, v any) error
	}{
		{"PutCtx", true, func(s *FileStore, v any) error { return s.PutCtx(ctx, "k", v, time.Hour) }},
		{"ForeverCtx", true, func(s *FileStore, v any) error { return s.ForeverCtx(ctx, "k", v) }},
		{"AddCtx", false, func(s *FileStore, v any) error {
			ok, err := s.AddCtx(ctx, "k", v, time.Hour)
			if err == nil && !ok {
				t.Errorf("AddCtx of an absent key inserted nothing")
			}
			return err
		}},
	}
	for _, w := range writes {
		for _, mode := range hostile.Modes() {
			t.Run(w.name+"/"+mode.String(), func(t *testing.T) {
				s := newSharedFileStores(t, 1)[0]
				before, hadBefore := any(nil), false
				if w.prior {
					if err := s.PutCtx(ctx, "k", "v0", time.Hour); err != nil {
						t.Fatalf("PutCtx: %v", err)
					}
					before, hadBefore = "v0", true
				}
				var reentered error
				code := hostile.New(t, mode, func() {
					if v, ok := s.GetCtx(ctx, "k"); ok != hadBefore || v != before {
						t.Errorf("GetCtx from MarshalJSON = %v, %v", v, ok)
					}
					reentered = s.PutCtx(ctx, "other", "o", time.Hour)
				})
				type result struct {
					err      error
					panicked any
				}
				done := make(chan result, 1)
				go func() { //safe-goroutine: the write under test; its result and panic are read below
					var r result
					defer func() {
						r.panicked = recover()
						done <- r
					}()
					r.err = w.write(s, codeMarshaler{code, "v1"})
				}()
				if mode == hostile.Block {
					if !code.AwaitEntered(t) {
						return
					}
					hostile.Within(t, hostile.Deadline, func() {
						if v, ok := s.GetCtx(ctx, "k"); ok != hadBefore || v != before {
							t.Errorf("GetCtx while MarshalJSON blocks = %v, %v", v, ok)
						}
						if err := s.PutCtx(ctx, "side", "s", time.Hour); err != nil {
							t.Errorf("PutCtx while MarshalJSON blocks: %v", err)
						}
					})
					code.Release()
				}
				var r result
				hostile.Within(t, hostile.Deadline, func() { r = <-done })
				if code.Calls() == 0 {
					t.Fatal("MarshalJSON never ran")
				}
				want, wantFound := any("v1"), true
				switch mode {
				case hostile.Panic:
					if r.panicked != hostile.PanicValue {
						t.Fatalf("panic = %v, want MarshalJSON's", r.panicked)
					}
					want, wantFound = before, hadBefore
				default:
					if r.err != nil || r.panicked != nil || reentered != nil {
						t.Fatalf("%s = %v, panic %v (re-entered: %v); want a write", w.name, r.err, r.panicked, reentered)
					}
				}
				hostile.Within(t, hostile.Deadline, func() {
					if v, ok := s.GetCtx(ctx, "k"); ok != wantFound || v != want {
						t.Errorf("k = %v, %v; want %v, %v", v, ok, want, wantFound)
					}
					if err := s.PutCtx(ctx, "k", "v2", time.Hour); err != nil {
						t.Errorf("a later PutCtx: %v", err)
					}
				})
			})
		}
	}
}

func BenchmarkFileStore_Put(b *testing.B) {
	dir := b.TempDir()
	s, err := NewFileStoreWithOptions("bench", dir, time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	ctx := context.Background()
	v := map[string]any{"n": 1.0, "s": "x"}
	b.ReportAllocs()
	for b.Loop() {
		if err := s.PutCtx(ctx, "k", v, time.Hour); err != nil {
			b.Fatal(err)
		}
	}
}
