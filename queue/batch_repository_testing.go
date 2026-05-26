package queue

// ResetDefaultBatchRepositoryForTest restores the process-wide default
// batch repository to a fresh in-memory instance with userSet=false so
// the auto-install path inside EnsureDefaultBatchRepository will fire
// again on the next call.
//
// Intended ONLY for use by Velocity's own integration tests that need
// to assert what the framework's boot wiring does when no custom repo
// has been installed. It is exported so the root-package tests
// (queue_batch_autoinstall_test.go) can call it without importing
// queue-internal symbols. There is no production code path that would
// want this helper - resetting a live repository drops in-flight
// batch state on the floor.
//
// The previous repository (if any) is closed to drain its cleanup
// goroutine and avoid leaks.
func ResetDefaultBatchRepositoryForTest() {
	prev := defaultBatchRepo.Load()
	defaultBatchRepo.Store(&batchRepoHolder{BatchRepository: NewInMemoryBatchRepository()})
	if prev != nil && prev.BatchRepository != nil {
		_ = prev.BatchRepository.Close()
	}

	// Clear any callback closures registered by earlier tests so the
	// reset is truly "blank slate". Closures keyed by stale BatchIDs
	// would otherwise outlive their batches and confuse later tests.
	globalCallbacks.mu.Lock()
	globalCallbacks.entries = make(map[BatchID]*callbackEntry)
	globalCallbacks.mu.Unlock()
}
