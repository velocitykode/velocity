package nilval

import "testing"

type impl struct{}

func (*impl) M() {}

type iface interface{ M() }

func TestIs(t *testing.T) {
	var p *impl
	var i iface = p
	var m map[string]int
	var s []int
	var f func()
	var c chan int
	tests := []struct {
		name string
		v    any
		want bool
	}{
		{"untyped nil", nil, true},
		{"typed nil pointer", p, true},
		{"typed nil behind an interface", i, true},
		{"nil map", m, true},
		{"nil slice", s, true},
		{"nil func", f, true},
		{"nil chan", c, true},
		{"pointer", &impl{}, false},
		{"struct", impl{}, false},
		{"zero int", 0, false},
		{"empty string", "", false},
		{"empty slice", []int{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Is(tt.v); got != tt.want {
				t.Errorf("Is(%#v) = %v, want %v", tt.v, got, tt.want)
			}
		})
	}
}

// BenchmarkIs is held to zero allocs/op by
// scripts/ci/check-zero-alloc-benchmarks.sh.
func BenchmarkIs(b *testing.B) {
	v := &impl{}
	var p *impl
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Is(v)
		_ = Is(p)
		_ = Is(nil)
	}
}
