package drivers

import (
	"testing"
)

func BenchmarkFileLogger_Info(b *testing.B) {
	tempDir := b.TempDir()
	logger := NewFileLogger(tempDir)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info("benchmark message", "iteration", i, "key", "value")
	}
}

func BenchmarkConsoleLogger_Info(b *testing.B) {
	logger := NewConsoleLogger()
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info("benchmark message", "iteration", i, "key", "value")
	}
}

func BenchmarkFileLogger_Parallel(b *testing.B) {
	tempDir := b.TempDir()
	logger := NewFileLogger(tempDir)
	
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			logger.Info("parallel benchmark", "iteration", i, "key", "value")
			i++
		}
	})
}

func BenchmarkConsoleLogger_Parallel(b *testing.B) {
	logger := NewConsoleLogger()
	
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			logger.Info("parallel benchmark", "iteration", i, "key", "value")
			i++
		}
	})
}