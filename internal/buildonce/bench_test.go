package buildonce

import (
	"context"
	"testing"
)

// deep calls fn from n frames further down the stack.
func deep(n int, fn func()) {
	if n == 0 {
		fn()
		return
	}
	deep(n-1, fn)
}

// BenchmarkDo_Uncontended is a Do that finds no build of its key in
// flight: the path every first use of a name takes.
func BenchmarkDo_Uncontended(b *testing.B) {
	for _, depth := range []struct {
		name   string
		frames int
	}{{"bench-depth", 0}, {"plus-30-frames", 30}} {
		b.Run(depth.name, func(b *testing.B) {
			var g Group[int]
			ctx := context.Background()
			build := func() (int, error) { return 1, nil }
			b.ReportAllocs()
			deep(depth.frames, func() {
				for b.Loop() {
					if _, err := g.Do(ctx, "k", build); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// BenchmarkDo_Contended is a Do that finds its key's build in flight on
// another goroutine: it checks it is not inside that build, joins it and,
// its ctx already ended, returns.
func BenchmarkDo_Contended(b *testing.B) {
	for _, depth := range []struct {
		name   string
		frames int
	}{{"bench-depth", 0}, {"plus-30-frames", 30}} {
		b.Run(depth.name, func(b *testing.B) {
			var g Group[int]
			started := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			go func() {
				_, _ = g.Do(context.Background(), "k", func() (int, error) {
					close(started)
					<-release
					return 1, nil
				})
			}()
			<-started
			ended, cancel := context.WithCancel(context.Background())
			cancel()
			build := func() (int, error) { return 0, nil }
			b.ReportAllocs()
			deep(depth.frames, func() {
				for b.Loop() {
					if _, err := g.Do(ended, "k", build); err == nil {
						b.Fatal("a contended Do with an ended ctx returned no error")
					}
				}
			})
		})
	}
}
