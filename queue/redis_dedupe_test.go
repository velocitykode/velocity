package queue

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// newMiniRedisDriver spins up a fresh miniredis backing a RedisDriver
// configured to talk to it. The driver and the miniredis handle are
// returned together because tests need direct access to both: the
// driver for the operation under test and the miniredis instance for
// state assertions (TTL, list contents) and fault injection
// (SetError, script flush).
func newMiniRedisDriver(t *testing.T) (*RedisDriver, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	driver, err := NewRedisDriver(RedisConfig{
		Host: mr.Host(),
		Port: mr.Port(),
		DB:   "0",
	})
	if err != nil {
		t.Fatalf("new redis driver: %v", err)
	}
	t.Cleanup(func() { _ = driver.Shutdown(context.Background()) })
	return driver, mr
}

// TestRedisDriver_PushIfNotExistsCtx_AtomicViaScript is the C-03 fb5
// regression: a second push with the same dedupe key must not produce
// a second RPUSH, the sentinel TTL must not be refreshed (because the
// script only SETs the sentinel when the EXISTS check returns 0), and
// both halves of the operation (sentinel SET + RPUSH) must commit
// together on the first call.
func TestRedisDriver_PushIfNotExistsCtx_AtomicViaScript(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil) // simplify payload verification surface

	driver, mr := newMiniRedisDriver(t)

	job1 := &BatchCallbackJob{BatchID: "batch_atomic", Kind: CallbackThen, Name: "noop"}
	dedupe := job1.DedupeKey()

	// First push: must create the sentinel AND queue the payload in
	// the same atomic step.
	if err := driver.PushIfNotExistsCtx(context.Background(), job1, dedupe, "default"); err != nil {
		t.Fatalf("first push: %v", err)
	}

	sentinelKey := driver.getDedupeKey(dedupe)
	queueKey := driver.getQueueKey("default")

	if !mr.Exists(sentinelKey) {
		t.Fatal("sentinel must exist after first push")
	}
	firstTTL := mr.TTL(sentinelKey)
	if firstTTL <= 0 {
		t.Errorf("sentinel TTL must be positive after SET ... EX; got %s", firstTTL)
	}
	// Sanity: the queue list now has exactly one entry.
	entries, err := mr.List(queueKey)
	if err != nil {
		t.Fatalf("inspect queue list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("after first push, queue must have 1 entry; got %d", len(entries))
	}

	// Advance the simulated clock so a refresh of the TTL would be
	// visible as a clock jump back to ~7d. The script's SET only runs
	// on the "EXISTS == 0" branch so the second push MUST NOT refresh.
	mr.FastForward(2 * time.Hour)
	ttlAfterFastForward := mr.TTL(sentinelKey)

	// Second push with identical dedupe key: must be a no-op. The
	// script returns 0; no second RPUSH; sentinel TTL untouched.
	job2 := &BatchCallbackJob{BatchID: "batch_atomic", Kind: CallbackThen, Name: "noop"}
	if err := driver.PushIfNotExistsCtx(context.Background(), job2, dedupe, "default"); err != nil {
		t.Fatalf("second push: %v", err)
	}

	entries, err = mr.List(queueKey)
	if err != nil {
		t.Fatalf("re-inspect queue list: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("second push must not append a second queue entry; got %d", len(entries))
	}

	// TTL must not have been refreshed: it should equal (within a
	// reasonable margin) the TTL observed AFTER the FastForward, not
	// the original TTL. We check that the second push did not push
	// TTL back up to the 7d window.
	ttlAfter := mr.TTL(sentinelKey)
	if ttlAfter > ttlAfterFastForward+time.Second {
		t.Errorf("second push refreshed sentinel TTL (got %s, want <= %s); script EXISTS-branch must be no-op",
			ttlAfter, ttlAfterFastForward)
	}
}

// TestRedisDriver_PushIfNotExistsCtx_NoScriptReload exercises the
// EVALSHA->NOSCRIPT->EVAL fallback that go-redis's *Script.Run
// performs transparently. We push once (loads the script), flush the
// server's script cache (SCRIPT FLUSH simulates a Redis restart
// without losing the keyspace), then push with a NEW dedupe key. The
// second call MUST succeed: NOSCRIPT triggers a transparent EVAL,
// which re-caches the SHA1, after which subsequent calls go through
// EVALSHA cleanly. If the fallback were broken, the second push would
// return a NOSCRIPT error.
func TestRedisDriver_PushIfNotExistsCtx_NoScriptReload(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, mr := newMiniRedisDriver(t)

	jobA := &BatchCallbackJob{BatchID: "batch_no_a", Kind: CallbackThen, Name: "noop"}
	if err := driver.PushIfNotExistsCtx(context.Background(), jobA, jobA.DedupeKey(), "default"); err != nil {
		t.Fatalf("first push (loads script): %v", err)
	}

	// Flush the server-side script cache to force the next EVALSHA
	// reply with NOSCRIPT. The client-side ScriptFlush issues the
	// SCRIPT FLUSH command; miniredis supports it and removes every
	// cached SHA1. The next *Script.Run on any driver instance sees
	// NOSCRIPT and transparently retries with EVAL.
	if err := driver.client.ScriptFlush(context.Background()).Err(); err != nil {
		t.Fatalf("SCRIPT FLUSH: %v", err)
	}

	// Different key so the EXISTS branch returns 0 and the script
	// has to actually run end-to-end. After FLUSH the first EVALSHA
	// returns NOSCRIPT; *Script.Run transparently falls back to EVAL.
	jobB := &BatchCallbackJob{BatchID: "batch_no_b", Kind: CallbackThen, Name: "noop"}
	if err := driver.PushIfNotExistsCtx(context.Background(), jobB, jobB.DedupeKey(), "default"); err != nil {
		t.Fatalf("push after SCRIPT FLUSH (NOSCRIPT fallback): %v", err)
	}
	if !mr.Exists(driver.getDedupeKey(jobB.DedupeKey())) {
		t.Error("post-flush push must still create the sentinel via EVAL fallback")
	}

	// A third push (different key again) should now go through
	// EVALSHA cleanly because Eval re-caches the SHA1. We cannot
	// directly assert which command was used without instrumenting
	// the client; success is sufficient.
	jobC := &BatchCallbackJob{BatchID: "batch_no_c", Kind: CallbackThen, Name: "noop"}
	if err := driver.PushIfNotExistsCtx(context.Background(), jobC, jobC.DedupeKey(), "default"); err != nil {
		t.Fatalf("third push (post-NOSCRIPT recovery): %v", err)
	}
}

// TestRedisDriver_PushIfNotExistsCtx_AmbiguousNetworkError simulates
// the failure mode that drove this fix: an error returned from the
// EVAL/EVALSHA round trip. The driver MUST surface the error AND
// leave the dedupe state coherent: either the entire script ran (so
// a retry no-ops) or none of it ran (so a retry inserts as fresh).
//
// We force the error by injecting it via miniredis.SetError, which
// makes every subsequent command return the given ERR reply. The
// previous SETNX+RPUSH+rollback implementation would have observed a
// (set-then-error) sequence and left the sentinel orphaned; the Lua
// script is all-or-nothing, so neither key is touched on the failed
// call.
func TestRedisDriver_PushIfNotExistsCtx_AmbiguousNetworkError(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, mr := newMiniRedisDriver(t)

	mr.SetError("simulated transient network error")

	job := &BatchCallbackJob{BatchID: "batch_neterr", Kind: CallbackThen, Name: "noop"}
	dedupe := job.DedupeKey()
	err := driver.PushIfNotExistsCtx(context.Background(), job, dedupe, "default")
	if err == nil {
		t.Fatal("expected an error when miniredis is in SetError mode")
	}
	if err.Error() == "" {
		t.Errorf("error must carry a non-empty message, got %v", err)
	}

	// Clear the error state and verify the dedupe state is coherent:
	// neither the sentinel nor a queue entry exists. The previous
	// implementation could have left an orphaned sentinel (SETNX
	// succeeded server-side, RPUSH failed, rollback DEL also failed)
	// or vice versa; the Lua script cannot leave either half behind.
	mr.SetError("")
	if mr.Exists(driver.getDedupeKey(dedupe)) {
		t.Error("sentinel must not be left orphaned after a transport error")
	}
	entries, _ := mr.List(driver.getQueueKey("default"))
	if len(entries) != 0 {
		t.Errorf("queue list must remain empty after a failed push; got %d entries", len(entries))
	}

	// And a retry must now succeed cleanly.
	if err := driver.PushIfNotExistsCtx(context.Background(), job, dedupe, "default"); err != nil {
		t.Fatalf("retry after transient error: %v", err)
	}
	if !mr.Exists(driver.getDedupeKey(dedupe)) {
		t.Error("retry must create the sentinel")
	}
	entries, _ = mr.List(driver.getQueueKey("default"))
	if len(entries) != 1 {
		t.Errorf("retry must enqueue exactly 1 payload; got %d", len(entries))
	}
}

// TestRedisDriver_PushIfNotExistsCtx_RPUSHErrorRollsBackSentinel is the
// C-03 fb6 regression. Lua atomicity is NOT Redis transactionality: a
// runtime error inside `redis.call('RPUSH', ...)` raises a Lua error
// after the prior SET has already mutated state, so without the pcall
// wrapper the sentinel survives even though no queue entry was created.
// The reaper's EXISTS branch would then no-op every subsequent retry
// for the full 7d TTL.
//
// We trigger the failure deterministically by pre-poisoning the queue
// key as a string. RPUSH on a key holding a string value returns
// WRONGTYPE, which the script's `pcall` catches; the rollback DEL
// runs in the same atomic execution, clearing the sentinel before the
// script returns its error reply.
//
// Post-conditions: PushIfNotExistsCtx returns an error, the sentinel
// is gone (so the reaper can retry), the queue key is unchanged
// (still the poisoned string).
func TestRedisDriver_PushIfNotExistsCtx_RPUSHErrorRollsBackSentinel(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, mr := newMiniRedisDriver(t)

	queueKey := driver.getQueueKey("default")

	// Poison: set the queue key to a string so RPUSH errors with
	// WRONGTYPE inside the script.
	if err := mr.Set(queueKey, "not-a-list"); err != nil {
		t.Fatalf("seed poison key: %v", err)
	}

	job := &BatchCallbackJob{BatchID: "batch_wrongtype", Kind: CallbackThen, Name: "noop"}
	dedupe := job.DedupeKey()
	sentinelKey := driver.getDedupeKey(dedupe)

	err := driver.PushIfNotExistsCtx(context.Background(), job, dedupe, "default")
	if err == nil {
		t.Fatal("expected an error when RPUSH targets a string key")
	}

	// The CRITICAL invariant: the sentinel must NOT outlive the
	// failed push. Without the pcall+DEL pair, the SET would persist
	// for the full 7d TTL and every subsequent reaper retry would
	// hit the EXISTS=1 short-circuit.
	if mr.Exists(sentinelKey) {
		t.Fatalf("sentinel %q must be rolled back after RPUSH error; instead it survives. "+
			"Lua atomicity is not transactionality; pcall+DEL is required.",
			sentinelKey)
	}

	// The poisoned queue key was not corrupted further. (Sanity:
	// RPUSH against a string never mutates the string.)
	got, err := mr.Get(queueKey)
	if err != nil {
		t.Fatalf("re-read poisoned queue key: %v", err)
	}
	if got != "not-a-list" {
		t.Errorf("poisoned queue key was mutated by failed RPUSH: got %q", got)
	}

	// Once the poison is cleared, the next push must succeed exactly
	// as if no prior attempt had happened. This proves the rollback
	// left no residual state.
	if !mr.Del(queueKey) {
		t.Fatalf("clear poison: key %q did not exist for delete", queueKey)
	}
	if err := driver.PushIfNotExistsCtx(context.Background(), job, dedupe, "default"); err != nil {
		t.Fatalf("clean retry after rollback: %v", err)
	}
	if !mr.Exists(sentinelKey) {
		t.Error("clean retry must create the sentinel")
	}
	entries, _ := mr.List(queueKey)
	if len(entries) != 1 {
		t.Errorf("clean retry must enqueue exactly 1 payload; got %d", len(entries))
	}
}
