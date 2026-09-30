package goroutine

import "testing"

//go:noinline
func outerNoInline(fn func()) { fn() }

func outerInlined(fn func()) { fn() }

func nest(n int, fn func()) {
	if n == 0 {
		fn()
		return
	}
	nest(n-1, fn)
}

func TestInside(t *testing.T) {
	noInline, inlined := FuncName(outerNoInline), FuncName(outerInlined)
	if Inside(noInline, inlined) {
		t.Fatal("Inside outside either function")
	}
	var got bool
	outerNoInline(func() { got = Inside(noInline) })
	if !got {
		t.Error("not Inside a function on the stack")
	}
	got = false
	outerInlined(func() { got = Inside(inlined) })
	if !got {
		t.Error("not Inside an inlined function on the stack")
	}
	got = false
	outerNoInline(func() { nest(500, func() { got = Inside(noInline) }) })
	if !got {
		t.Error("not Inside a function 500 frames up (the buffer must grow)")
	}
	outerNoInline(func() {
		if Inside(inlined) {
			t.Error("Inside a function not on the stack")
		}
	})
}

func BenchmarkInside(b *testing.B) {
	name := FuncName(outerNoInline)
	for _, depth := range []int{0, 30} {
		b.Run(map[int]string{0: "bench-depth", 30: "plus-30-frames"}[depth], func(b *testing.B) {
			b.ReportAllocs()
			nest(depth, func() {
				for b.Loop() {
					_ = Inside(name)
				}
			})
		})
	}
}
