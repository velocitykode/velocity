package drivers

import (
	"testing"

	"github.com/velocitykode/velocity/contract"
)

func BenchmarkConsoleLogger_Info(b *testing.B) {
	logger := NewConsoleLogger(contract.LogLevelDebug)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info("benchmark message", "iteration", i, "key", "value")
	}
}

func BenchmarkConsoleLogger_Parallel(b *testing.B) {
	logger := NewConsoleLogger(contract.LogLevelDebug)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			logger.Info("parallel benchmark", "iteration", i, "key", "value")
			i++
		}
	})
}
