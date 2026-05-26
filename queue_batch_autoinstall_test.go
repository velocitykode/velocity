package velocity

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/queue"
)

// TestQueueBatch_AutoInstallsDatabaseRepository asserts the C-03
// follow-up wiring: when Config.Queue.Driver = "database" and a DB
// connection is configured, velocity.New must replace the package-init
// in-memory batch repository with a DatabaseBatchRepository so that
// multi-host workers observe shared batch state. Without the auto-
// install, the user has to remember to call queue.SetDefaultBatchRepository
// from main(), and stock installs silently fall back to the in-memory
// default - which is exactly what the original C-03 finding hit.
func TestQueueBatch_AutoInstallsDatabaseRepository(t *testing.T) {
	// Restore the default repository on test exit so we don't pollute
	// sibling tests. The repo we install during the test will be closed
	// by SetDefaultBatchRepository's swap path.
	prev := queue.DefaultBatchRepository()
	t.Cleanup(func() { queue.SetDefaultBatchRepository(prev) })

	// Reset to a clean in-memory holder (userSet=false) so the auto-
	// install path actually fires; otherwise an earlier test that
	// called SetDefaultBatchRepository would leave userSet=true and the
	// Ensure helper would short-circuit.
	resetBatchRepoForTest()

	cfg := Config{
		Env:   "testing",
		Debug: true,
		Port:  "0",
		Cache: CacheConfig{Driver: "memory", Prefix: "test_cache"},
		Log: log.LogConfig{
			Driver: "console",
			Config: make(map[string]any),
		},
		Queue: QueueConfig{Driver: "database"},
		DB: DBConfig{
			Connection: "sqlite",
			Database:   ":memory:",
		},
		Mail: mail.MailConfig{Driver: "log"},
	}

	app, err := New(WithConfig(cfg))
	if err != nil {
		t.Fatalf("velocity.New: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	// The post-boot default must be the DatabaseBatchRepository, not
	// the package-init in-memory placeholder. We assert by concrete
	// type because the interface is identical between the two impls.
	got := queue.DefaultBatchRepository()
	if _, ok := got.(*queue.DatabaseBatchRepository); !ok {
		t.Fatalf("expected DefaultBatchRepository() to be *queue.DatabaseBatchRepository, got %T", got)
	}
}

// TestQueueBatch_AutoInstall_PreservesCustomRepo verifies the
// idempotence requirement: a custom repo installed before velocity.New
// must not be clobbered by the auto-install path. Apps that use, say,
// a Redis-backed batch repo for a Redis queue should be free to wire
// it up early without fearing the framework will overwrite it on boot.
func TestQueueBatch_AutoInstall_PreservesCustomRepo(t *testing.T) {
	prev := queue.DefaultBatchRepository()
	t.Cleanup(func() { queue.SetDefaultBatchRepository(prev) })

	resetBatchRepoForTest()

	// Pre-install a marker repository so we can detect it survived.
	marker := queue.NewInMemoryBatchRepository()
	queue.SetDefaultBatchRepository(marker)

	cfg := Config{
		Env:   "testing",
		Debug: true,
		Port:  "0",
		Cache: CacheConfig{Driver: "memory", Prefix: "test_cache"},
		Log: log.LogConfig{
			Driver: "console",
			Config: make(map[string]any),
		},
		Queue: QueueConfig{Driver: "database"},
		DB: DBConfig{
			Connection: "sqlite",
			Database:   ":memory:",
		},
		Mail: mail.MailConfig{Driver: "log"},
	}

	app, err := New(WithConfig(cfg))
	if err != nil {
		t.Fatalf("velocity.New: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	got := queue.DefaultBatchRepository()
	if got != marker {
		t.Fatalf("expected pre-installed repo to survive auto-install; got %T (want %T)", got, marker)
	}
}

// TestQueueBatch_AutoInstall_MemoryDriverLeavesDefault confirms the
// auto-install does NOT fire for the memory queue driver. Tests and
// single-host installs should keep the in-process default so we don't
// require a DB just to use batches.
func TestQueueBatch_AutoInstall_MemoryDriverLeavesDefault(t *testing.T) {
	prev := queue.DefaultBatchRepository()
	t.Cleanup(func() { queue.SetDefaultBatchRepository(prev) })

	resetBatchRepoForTest()

	cfg := Config{
		Env:   "testing",
		Debug: true,
		Port:  "0",
		Cache: CacheConfig{Driver: "memory", Prefix: "test_cache"},
		Log: log.LogConfig{
			Driver: "console",
			Config: make(map[string]any),
		},
		Queue: QueueConfig{Driver: "memory"},
		Mail:  mail.MailConfig{Driver: "log"},
	}

	app, err := New(WithConfig(cfg))
	if err != nil {
		t.Fatalf("velocity.New: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	got := queue.DefaultBatchRepository()
	if _, ok := got.(*queue.DatabaseBatchRepository); ok {
		t.Fatalf("memory queue driver should not install DatabaseBatchRepository, but got %T", got)
	}
}

// resetBatchRepoForTest reaches into the queue package via the exported
// test helper to restore a fresh in-memory holder with userSet=false.
// Implemented in queue/batch_repository_test.go as a build-tagged
// helper so we don't have to expose userSet manipulation to consumers.
func resetBatchRepoForTest() {
	queue.ResetDefaultBatchRepositoryForTest()
}
