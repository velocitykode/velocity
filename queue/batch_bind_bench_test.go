package queue

import (
	"fmt"
	"testing"
)

// BenchmarkBindBatchJobs measures the pass that binds a batch's jobs
// before the batch is saved: one allocation per batch (the resolved queue
// names), none per job.
func BenchmarkBindBatchJobs(b *testing.B) {
	jobs := make([]Job, 16)
	for i := range jobs {
		jobs[i] = &testOnQueuerJob{queue: fmt.Sprintf("q%d", i%2)}
	}
	id := newBatchID()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := bindBatchJobs(jobs, id, "q-batch"); err != nil {
			b.Fatal(err)
		}
	}
}
