package async

import (
	"testing"
	"time"
)

func BenchmarkRun(b *testing.B) {
	for i := 0; i < b.N; i++ {
		result := Run(func() int { return 42 })
		result.Get()
	}
}

func BenchmarkRunWithGoroutine(b *testing.B) {
	// Compare with raw goroutine
	for i := 0; i < b.N; i++ {
		done := make(chan int, 1)
		go func() {
			done <- 42
		}()
		<-done
	}
}

func BenchmarkAll(b *testing.B) {
	fns := []func() int{
		func() int { return 1 },
		func() int { return 2 },
		func() int { return 3 },
		func() int { return 4 },
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = All(fns...)
	}
}

func BenchmarkAllSequential(b *testing.B) {
	// Compare with sequential execution
	fns := []func() int{
		func() int { return 1 },
		func() int { return 2 },
		func() int { return 3 },
		func() int { return 4 },
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results := make([]int, len(fns))
		for j, fn := range fns {
			results[j] = fn()
		}
	}
}

func BenchmarkRace(b *testing.B) {
	fns := []func() int{
		func() int { time.Sleep(1 * time.Microsecond); return 1 },
		func() int { time.Sleep(2 * time.Microsecond); return 2 },
		func() int { time.Sleep(3 * time.Microsecond); return 3 },
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := Race(fns...)
		result.Get()
	}
}

func BenchmarkForEach(b *testing.B) {
	items := make([]int, 100)
	for i := range items {
		items[i] = i
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ForEach(items, 10, func(item int) {
			// Simulate work
			_ = item * 2
		})
	}
}

func BenchmarkMap(b *testing.B) {
	items := make([]int, 100)
	for i := range items {
		items[i] = i
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Map(items, func(item int) int {
			return item * 2
		})
	}
}

func BenchmarkMapN(b *testing.B) {
	items := make([]int, 100)
	for i := range items {
		items[i] = i
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = MapN(16, items, func(item int) int {
			return item * 2
		})
	}
}

func BenchmarkGo(b *testing.B) {
	for i := 0; i < b.N; i++ {
		done := make(chan struct{})
		Go(func() {
			close(done)
		})
		<-done
	}
}

// Memory benchmarks
func BenchmarkRunMemory(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		result := Run(func() int { return 42 })
		result.Get()
	}
}

func BenchmarkAllMemory(b *testing.B) {
	b.ReportAllocs()
	fns := []func() int{
		func() int { return 1 },
		func() int { return 2 },
		func() int { return 3 },
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = All(fns...)
	}
}
