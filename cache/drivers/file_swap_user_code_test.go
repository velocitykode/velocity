//go:build unix

package drivers

import (
	"context"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// codeMarshaler is a value whose MarshalJSON is user code: it runs code,
// then encodes as text.
type codeMarshaler struct {
	code *hostile.Code
	text string
}

func (m codeMarshaler) MarshalJSON() ([]byte, error) {
	m.code.Run()
	return []byte(`"` + m.text + `"`), nil
}

// TestFileStore_CompareAndSwap_ExpectedMarshalerIsUserCode swaps with an
// expected value whose MarshalJSON is each kind of hostile user code. The
// expected value is encoded before the key lock and the store mutex are
// taken: a MarshalJSON reading or writing the same store returns, a
// blocked one holds up no other call on the store, and a panicking one
// reaches the caller and leaves the store usable.
func TestFileStore_CompareAndSwap_ExpectedMarshalerIsUserCode(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			s := newSharedFileStores(t, 1)[0]
			ctx := context.Background()
			if err := s.PutCtx(ctx, "k", "v0", time.Hour); err != nil {
				t.Fatalf("PutCtx: %v", err)
			}
			var reentered error
			code := hostile.New(t, mode, func() {
				if v, ok := s.GetCtx(ctx, "k"); !ok || v != "v0" {
					t.Errorf("GetCtx from MarshalJSON = %v, %v", v, ok)
				}
				reentered = s.PutCtx(ctx, "other", "o", time.Hour)
			})
			type result struct {
				ok       bool
				err      error
				panicked any
			}
			done := make(chan result, 1)
			go func() { //safe-goroutine: the swap under test; its result and panic are read below
				var r result
				defer func() {
					r.panicked = recover()
					done <- r
				}()
				r.ok, r.err = s.CompareAndSwapCtx(ctx, "k", codeMarshaler{code, "v0"}, "v1", time.Hour)
			}()
			if mode == hostile.Block {
				if !code.AwaitEntered(t) {
					return
				}
				hostile.Within(t, hostile.Deadline, func() {
					if v, ok := s.GetCtx(ctx, "k"); !ok || v != "v0" {
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
			want := "v1"
			switch mode {
			case hostile.Panic:
				if r.panicked != hostile.PanicValue {
					t.Fatalf("panic = %v, want MarshalJSON's", r.panicked)
				}
				want = "v0"
			default:
				if !r.ok || r.err != nil || reentered != nil {
					t.Fatalf("CompareAndSwapCtx = %v, %v (re-entered: %v); want a swap", r.ok, r.err, reentered)
				}
			}
			hostile.Within(t, hostile.Deadline, func() {
				if v, ok := s.GetCtx(ctx, "k"); !ok || v != want {
					t.Errorf("k = %v, %v; want %q", v, ok, want)
				}
				if ok, err := s.CompareAndSwapCtx(ctx, "k", want, "v2", time.Hour); !ok || err != nil {
					t.Errorf("a later swap = %v, %v", ok, err)
				}
			})
		})
	}
}

// TestFileStore_CompareAndSwap_ExpectedErrorOnlyWhenCompared keeps the
// result of a swap whose expected value cannot be encoded: an error when
// a live value is there to compare with it, (false, nil) when the key is
// absent, as before the encoding moved out of the lock.
func TestFileStore_CompareAndSwap_ExpectedErrorOnlyWhenCompared(t *testing.T) {
	s := newSharedFileStores(t, 1)[0]
	ctx := context.Background()
	bad := func() {}
	if ok, err := s.CompareAndSwapCtx(ctx, "absent", bad, "v", time.Hour); ok || err != nil {
		t.Fatalf("absent key: CompareAndSwapCtx = %v, %v; want false, nil", ok, err)
	}
	if err := s.PutCtx(ctx, "k", "v0", time.Hour); err != nil {
		t.Fatalf("PutCtx: %v", err)
	}
	if ok, err := s.CompareAndSwapCtx(ctx, "k", bad, "v", time.Hour); ok || err == nil {
		t.Fatalf("live key: CompareAndSwapCtx = %v, %v; want false and an error", ok, err)
	}
}

func BenchmarkFileStore_CompareAndSwap(b *testing.B) {
	dir := b.TempDir()
	s, err := NewFileStoreWithOptions("bench", dir, time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	ctx := context.Background()
	if err := s.PutCtx(ctx, "k", map[string]any{"n": 1.0, "s": "x"}, time.Hour); err != nil {
		b.Fatal(err)
	}
	v := map[string]any{"n": 1.0, "s": "x"}
	b.ReportAllocs()
	for b.Loop() {
		if ok, err := s.CompareAndSwapCtx(ctx, "k", v, v, time.Hour); !ok || err != nil {
			b.Fatal(ok, err)
		}
	}
}
