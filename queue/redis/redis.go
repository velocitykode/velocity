package redis

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/trace"
)

// init registers the redis queue driver into the canonical queue registry.
// The redis driver lives in this leaf package so the queue root never pulls
// in the go-redis dependency; importing this package (directly or via
// queue/standard) wires the "redis" factory.
func init() {
	queue.Drivers().Register("redis", func(_ context.Context, cfg queue.QueueConfig) (queue.Driver, error) {
		return NewRedisDriver(cfg.Redis)
	})
}

const redisPoppedAttemptsMaxEntries = 1024

// RedisDriver implements the queue.Driver interface using Redis.
type RedisDriver struct {
	// DriverCore supplies the lock-free event-dispatch slot (SetEventDispatcher
	// / DispatchEvent) shared by every built-in driver. Embedded so the
	// promoted SetEventDispatcher satisfies contract.EventDispatcherAware (the
	// conformance is asserted in contracts.go).
	queue.DriverCore

	client *redis.Client
	ctx    context.Context
	config queue.RedisConfig

	// poppedAttempts carries Payload.Attempts across the Redis delete-on-pop
	// boundary long enough for the worker's retry PushDelayedCtx call to
	// persist the next attempt count. Entries are best-effort metadata keyed
	// by the popped job object; PushDelayedCtx and Failed remove entries for
	// paths that terminate through the driver. Successful jobs do not route
	// back through Redis, so the map is bounded and opportunistically pruned
	// on pop.
	poppedAttempts      sync.Map // keyed by job object, value int
	poppedAttemptsCount int64

	// nonIdentifiableWarned ensures the advisory for jobs without a stable
	// JobID logs at most once per distinct job type.
	nonIdentifiableWarned sync.Map // keyed by job type string

	logger atomic.Value // holds redisLoggerHolder{Logger}
}

type redisLoggerHolder struct{ Logger queue.Logger }

type poppedAttemptsKey struct {
	typ reflect.Type
	ptr uintptr
}

// New constructs a Redis queue driver from cfg for standalone use without
// going through the queue driver registry. It returns the same driver the
// registry path produces, so both routes are equivalent.
func New(cfg queue.RedisConfig) (queue.Driver, error) {
	return NewRedisDriver(cfg)
}

// NewRedisDriver creates a new Redis queue driver.
func NewRedisDriver(config queue.RedisConfig) (*RedisDriver, error) {
	db, err := strconv.Atoi(config.DB)
	if err != nil {
		db = 0
	}

	opts := &redis.Options{
		Addr:     fmt.Sprintf("%s:%s", config.Host, config.Port),
		Password: config.Password,
		DB:       db,
	}

	// Enable TLS if configured
	if config.TLS {
		opts.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}

	client := redis.NewClient(opts)

	ctx := context.Background()

	// Test connection
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("velocity/queue: failed to connect to redis: %w", err)
	}

	// A non-loopback Redis without TLS sends every command (including the
	// AUTH password and job payloads) in cleartext, and without a password
	// anyone who can reach the host can read and inject jobs. Warn loudly
	// at startup; do not refuse to boot, since the operator may secure the
	// link elsewhere (VPC, tunnel). The driver's SetLogger seam is only
	// installed after construction, so this goes through slog like the
	// cache driver's startup warnings.
	warnIfInsecure(config.Host, config.Password, config.TLS)

	return &RedisDriver{
		client: client,
		ctx:    ctx,
		config: config,
	}, nil
}

// isLoopbackHost reports whether host clearly names the local machine:
// "localhost" or a literal loopback IP (127.0.0.0/8, ::1). Hostnames are
// deliberately not DNS-resolved at construction, so any other name counts
// as remote.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// warnIfInsecure logs a startup warning when the driver connects to a
// non-loopback host with TLS disabled or with no password.
func warnIfInsecure(host, password string, tlsEnabled bool) {
	if isLoopbackHost(host) {
		return
	}
	if !tlsEnabled {
		slog.Default().Warn(
			"velocity/queue: redis driver connecting to non-loopback host without TLS; traffic (including the password and job payloads) is sent in cleartext. Set REDIS_TLS=true or RedisConfig.TLS.",
			"host", host,
		)
	}
	if password == "" {
		slog.Default().Warn(
			"velocity/queue: redis driver connecting to non-loopback host without a password; anyone who can reach the host can read and inject jobs. Set QUEUE_REDIS_PASSWORD or RedisConfig.Password.",
			"host", host,
		)
	}
}

// SetLogger installs a logger for Redis-driver operational advisories. Nil
// disables logging. Safe to call concurrently.
func (r *RedisDriver) SetLogger(l queue.Logger) {
	r.logger.Store(redisLoggerHolder{Logger: l})
}

func (r *RedisDriver) log() queue.Logger {
	v := r.logger.Load()
	if v == nil {
		return nil
	}
	return v.(redisLoggerHolder).Logger
}

func (r *RedisDriver) warnIfNonIdentifiable(job queue.Job) {
	if _, ok := job.(queue.Identifiable); ok {
		return
	}
	typ := fmt.Sprintf("%T", job)
	if _, loaded := r.nonIdentifiableWarned.LoadOrStore(typ, struct{}{}); loaded {
		return
	}
	if logger := r.log(); logger != nil {
		logger.Warn("velocity/queue: job type does not implement Identifiable; MaxAttempts cannot be enforced reliably across redelivery. Implement queue.Identifiable.JobID() to fix.",
			"type", typ,
		)
	}
}

func (r *RedisDriver) rememberPoppedAttempts(job queue.Job, attempts int) {
	key, ok := redisPoppedAttemptsKey(job)
	if !ok {
		return
	}
	if _, loaded := r.poppedAttempts.LoadOrStore(key, attempts); loaded {
		r.poppedAttempts.Store(key, attempts)
		return
	}
	if atomic.AddInt64(&r.poppedAttemptsCount, 1) > redisPoppedAttemptsMaxEntries {
		r.prunePoppedAttempts(redisPoppedAttemptsMaxEntries / 2)
	}
}

func (r *RedisDriver) takePoppedAttempts(job queue.Job) (int, bool) {
	key, ok := redisPoppedAttemptsKey(job)
	if !ok {
		return 0, false
	}
	v, ok := r.poppedAttempts.LoadAndDelete(key)
	if !ok {
		return 0, false
	}
	atomic.AddInt64(&r.poppedAttemptsCount, -1)
	attempts, ok := v.(int)
	return attempts, ok
}

func (r *RedisDriver) prunePoppedAttempts(target int64) {
	excess := atomic.LoadInt64(&r.poppedAttemptsCount) - target
	if excess <= 0 {
		return
	}
	var deleted int64
	r.poppedAttempts.Range(func(key, _ any) bool {
		if deleted >= excess {
			return false
		}
		if _, ok := r.poppedAttempts.LoadAndDelete(key); ok {
			deleted++
		}
		return true
	})
	if deleted > 0 {
		atomic.AddInt64(&r.poppedAttemptsCount, -deleted)
	}
}

func redisPoppedAttemptsKey(job queue.Job) (interface{}, bool) {
	if job == nil {
		return nil, false
	}
	v := reflect.ValueOf(job)
	t := v.Type()
	if t.Kind() == reflect.Ptr {
		return poppedAttemptsKey{typ: t, ptr: v.Pointer()}, true
	}
	if t.Comparable() {
		return job, true
	}
	return nil, false
}

// PushCtx adds a job to the queue using the caller's context.
func (r *RedisDriver) PushCtx(ctx context.Context, job queue.Job, queueName ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := queue.ResolveQueueName(job, queueName...)
	queueKey := r.getQueueKey(name)

	payload, err := queue.SerializeJob(job, name)
	if err != nil {
		return err
	}
	payload.TraceID, payload.SpanID, payload.ParentID = trace.GetTraceContext(ctx)

	// Encrypt-then-sign: seal Data before marshalling so the signature
	// below covers the ciphertext (see queue/encryption.go).
	if err := queue.SealPayload(payload); err != nil {
		return err
	}

	data, err := queue.MarshalSigned(payload, func(sig string) { payload.Signature = sig },
		"velocity/queue: failed to marshal payload",
		"velocity/queue: failed to marshal signed payload")
	if err != nil {
		return err
	}

	if err := r.client.RPush(ctx, queueKey, data).Err(); err != nil {
		return err
	}

	queue.DispatchJobQueued(r.DispatchEvent, ctx, payload.Type, name, false, 0)
	return nil
}

// PushDelayedCtx adds a delayed job using the caller's context.
func (r *RedisDriver) PushDelayedCtx(ctx context.Context, job queue.Job, delay time.Duration, queueName ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := queue.ResolveQueueName(job, queueName...)
	delayedKey := r.getDelayedKey(name)

	payload, err := queue.SerializeJob(job, name)
	if err != nil {
		return err
	}
	if attempts, ok := r.takePoppedAttempts(job); ok {
		payload.Attempts = attempts + 1
	}
	payload.TraceID, payload.SpanID, payload.ParentID = trace.GetTraceContext(ctx)

	// Encrypt-then-sign: seal Data before marshalling so the signature
	// below covers the ciphertext (see queue/encryption.go).
	if err := queue.SealPayload(payload); err != nil {
		return err
	}

	data, err := queue.MarshalSigned(payload, func(sig string) { payload.Signature = sig },
		"velocity/queue: failed to marshal payload",
		"velocity/queue: failed to marshal signed payload")
	if err != nil {
		return err
	}

	score := float64(time.Now().Add(delay).Unix())
	if err := r.client.ZAdd(ctx, delayedKey, redis.Z{
		Score:  score,
		Member: data,
	}).Err(); err != nil {
		return err
	}

	queue.DispatchJobQueued(r.DispatchEvent, ctx, payload.Type, name, true, delay)
	return nil
}

// PopCtx retrieves and removes the next job, honouring ctx cancellation on
// the BLPOP round-trip so worker shutdown aborts without waiting the full
// BLPOP timeout.
func (r *RedisDriver) PopCtx(ctx context.Context, queueName string) (queue.Job, error) {
	job, _, err := r.PopCtxWithTrace(ctx, queueName)
	return job, err
}

// PopCtxWithTrace returns the popped job along with the producer-side trace
// context recovered from the persisted payload. Implements TraceAwareDriver.
//
// Poison quarantine: BLPop has already consumed the queue entry by the time
// hydration runs, so any unrecoverable failure during Unmarshal /
// verifyPayload / registry.Deserialize would silently drop the payload
// without the operator-visible breadcrumb the DB driver provides via its
// quarantineAndReturn path. To preserve parity with the DB driver, every
// such failure is routed through [RedisDriver.quarantinePoisonedPayload]:
// the raw bytes are written to a failed-jobs list keyed off the queue
// (`velocity:queue:<name>:failed`), a JobFailed event is dispatched so
// observers can alert, and the wrapped error includes ErrPoisonJob so
// workers treat it as a recoverable pop error rather than a hard failure.
func (r *RedisDriver) PopCtxWithTrace(ctx context.Context, queueName string) (queue.Job, queue.TraceContext, error) {
	var tc queue.TraceContext
	if err := ctx.Err(); err != nil {
		return nil, tc, err
	}
	// First, move any ready delayed jobs to the main queue
	if err := r.moveDelayedJobs(ctx, queueName); err != nil {
		return nil, tc, err
	}

	queueKey := r.getQueueKey(queueName)

	// Use BLPOP with a 1 second timeout for non-blocking behavior
	result, err := r.client.BLPop(ctx, 1*time.Second, queueKey).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, tc, nil // No jobs available
		}
		return nil, tc, err
	}

	if len(result) < 2 {
		return nil, tc, nil
	}

	rawPayload := result[1]

	var payload queue.Payload
	if err := json.Unmarshal([]byte(rawPayload), &payload); err != nil {
		return r.quarantinePoisonedPayload(ctx, queueName, rawPayload, "unknown",
			fmt.Errorf("velocity/queue: failed to unmarshal payload: %w", err))
	}

	// Verify payload integrity if signing is enabled.
	sig := payload.Signature
	payload.Signature = "" // Remove signature before verification
	verifyData, err := json.Marshal(payload)
	if err != nil {
		return r.quarantinePoisonedPayload(ctx, queueName, rawPayload, payload.Type,
			fmt.Errorf("velocity/queue: failed to marshal payload for verification: %w", err))
	}
	if err := queue.VerifyPayload(verifyData, sig); err != nil {
		return r.quarantinePoisonedPayload(ctx, queueName, rawPayload, payload.Type,
			fmt.Errorf("velocity/queue: queue integrity check failed: %w", err))
	}

	// Decrypt AFTER the signature check so verification never runs on
	// undecrypted attacker bytes (encrypt-then-sign; see
	// queue/encryption.go). sig != "" means a real signature verified
	// above, which gates the legacy-plaintext transition path inside
	// OpenPayload.
	if err := queue.OpenPayload(&payload, sig != ""); err != nil {
		return r.quarantinePoisonedPayload(ctx, queueName, rawPayload, payload.Type, err)
	}

	tc = queue.TraceContext{TraceID: payload.TraceID, SpanID: payload.SpanID, ParentID: payload.ParentID}

	// The dedupe sentinel is INTENTIONALLY NOT released here.
	// Holding the SETNX key past Pop is what keeps the at-most-once
	// contract robust against the C-03 fb4 failure mode where a
	// stale reaper attempts a re-push after the worker has already
	// consumed and executed the original. The key auto-expires after
	// 7 days (see PushIfNotExistsCtx), which is far past the typical
	// batch-completion window. Callers that need to release earlier
	// can DEL velocity:queue:dedupe:<key> directly.
	_ = payload.DedupeKey

	// Deserialize the job using the registry
	job, err := queue.Deserialize(&payload)
	if err != nil {
		j, qtc, qerr := r.quarantinePoisonedPayload(ctx, queueName, rawPayload, payload.Type,
			fmt.Errorf("velocity/queue: failed to deserialize job: %w", err))
		// Preserve the trace context recovered from the verified payload
		// even though the job itself could not be hydrated; observers
		// correlating the failure to the producer span need it.
		if qtc == (queue.TraceContext{}) {
			qtc = tc
		}
		return j, qtc, qerr
	}
	r.rememberPoppedAttempts(job, payload.Attempts)
	r.warnIfNonIdentifiable(job)
	return job, tc, nil
}

// quarantinePoisonedPayload mirrors the DB driver's quarantineAndReturn
// shape for the Redis driver: a raw BLPop payload that fails hydration
// (Unmarshal / verifyPayload / registry.Deserialize) is preserved in a
// per-queue failed-jobs list so an operator can inspect the bytes that
// poisoned the queue, a JobFailed event is dispatched so observers can
// alert, and the returned error wraps ErrPoisonJob so the worker treats
// the failure as recoverable (the entry is already gone from the live
// list; the next pop picks up the next eligible job).
//
// The payload (sealed first when encryption is on; see below) is
// stored as a base64-encoded blob alongside the
// queue name, error message, and timestamp. Base64 is used because the
// bytes that arrived on the wire are not necessarily valid UTF-8 (a
// classic poison vector), and storing them verbatim would corrupt the
// JSON envelope a human or tool would later read from the failed-jobs
// list.
//
// Write-failure handling: if the RPUSH that records the poison row
// itself fails (Redis down, OOM, etc.) the call still returns
// ErrPoisonJob joined with the original poison cause AND the secondary
// write error. The original entry is already consumed by BLPop so
// re-pushing it onto the live queue would either re-poison the worker
// loop in an infinite cycle or risk an additional duplicate; preserving
// the original error chain lets the worker log a complete forensic
// trail while still making progress on the next pop.
func (r *RedisDriver) quarantinePoisonedPayload(ctx context.Context, queueName, rawPayload, jobType string, poisonErr error) (queue.Job, queue.TraceContext, error) {
	var tc queue.TraceContext

	failedKey := r.getFailedKey(queueName)
	// With payload encryption enabled the poison bytes are sealed before
	// they reach the failed list (queue.SealQuarantineBlob): verbatim
	// plaintext in a long-lived Redis list would bypass the at-rest
	// confidentiality QUEUE_ENCRYPT promises. The "encrypted" marker tells
	// operator tooling to run the blob through queue.OpenQuarantineBlob
	// after base64-decoding.
	storedPayload, sealed := queue.SealQuarantineBlob(rawPayload)
	record := map[string]interface{}{
		"queue":       queueName,
		"payload_b64": base64.StdEncoding.EncodeToString([]byte(storedPayload)),
		"exception":   poisonErr.Error(),
		"failed_at":   time.Now().UTC(),
		"poison":      true,
	}
	if sealed {
		record["encrypted"] = true
	}
	data, merr := json.Marshal(record)

	var writeErr error
	switch {
	case merr != nil:
		// Defensive: time.Time + string keys marshal cleanly under
		// encoding/json, so this branch is essentially unreachable. We
		// still surface the failure so a future change to the record
		// shape cannot silently break quarantine bookkeeping.
		writeErr = fmt.Errorf("velocity/queue: failed to marshal poison record: %w", merr)
	default:
		// Use a detached, bounded context for the recovery write: the
		// caller's ctx may already be cancelled (worker shutdown is the
		// most common cause of BLPop returning a partial result) and
		// reusing it would guarantee the recovery RPUSH fails too,
		// dropping the breadcrumb we are trying to record. A fresh
		// background ctx with a short timeout gives recovery a chance
		// even mid-shutdown.
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.client.RPush(recoveryCtx, failedKey, data).Err(); err != nil {
			writeErr = fmt.Errorf("velocity/queue: failed to record poison row to %s: %w", failedKey, err)
		}
	}

	// Dispatch JobFailed so observers (APM, alerting) can react. We
	// dispatch even if the failed-jobs write failed because the event
	// stream is the higher-fidelity signal in degraded states: a Redis
	// outage that drops the RPUSH should still surface as JobFailed in
	// metrics, logs, and bus listeners.
	if jobType == "" {
		jobType = "unknown"
	}
	queue.DispatchJobFailed(r.DispatchEvent, ctx, jobType, queueName, poisonErr, 0)

	if writeErr != nil {
		return nil, tc, errors.Join(queue.ErrPoisonJob, poisonErr, writeErr)
	}
	return nil, tc, errors.Join(queue.ErrPoisonJob, poisonErr)
}

// Size returns the number of jobs in the queue
func (r *RedisDriver) Size(queueName string) (int64, error) {
	queueKey := r.getQueueKey(queueName)
	delayedKey := r.getDelayedKey(queueName)

	// Get the size of the main queue
	mainSize, err := r.client.LLen(r.ctx, queueKey).Result()
	if err != nil {
		return 0, err
	}

	// Get the size of the delayed queue
	delayedSize, err := r.client.ZCard(r.ctx, delayedKey).Result()
	if err != nil {
		return mainSize, nil // Return main size even if delayed fails
	}

	return mainSize + delayedSize, nil
}

// Clear removes all jobs from the queue (main list, delayed ZSET, and
// failed list).
//
// Dedupe sentinels are NOT cleared, a deliberate residual divergence
// from the memory and database drivers, which release queue-scoped
// dedupe state on Clear. The sentinel key is `velocity:queue:dedupe:<key>`
// (see getDedupeKey) and its value is a constant '1' (see
// redisDedupePushScript): neither the key nor the value records the
// owning queue, so a queue-scoped delete would require an unbounded
// SCAN of `velocity:queue:dedupe:*` and could still not attribute a
// sentinel to this queue. Rather than pay that cost, we let the
// sentinels lapse via their 7-day TTL. Consequence: a PushIfNotExistsCtx
// for a key pushed before Clear no-ops until the TTL expires. Dedupe
// keys are deterministic per (batchID, kind) with UUID v7 batch IDs, so
// this is not hit in practice.
func (r *RedisDriver) Clear(queueName string) error {
	queueKey := r.getQueueKey(queueName)
	delayedKey := r.getDelayedKey(queueName)
	failedKey := r.getFailedKey(queueName)

	pipe := r.client.Pipeline()
	pipe.Del(r.ctx, queueKey)
	pipe.Del(r.ctx, delayedKey)
	pipe.Del(r.ctx, failedKey)

	_, err := pipe.Exec(r.ctx)
	return err
}

// Failed moves a job to the failed queue
func (r *RedisDriver) Failed(job queue.Job, err error, queueName string) error {
	failedKey := r.getFailedKey(queueName)
	r.takePoppedAttempts(job)

	payload, serr := queue.SerializeJob(job, queueName)
	if serr != nil {
		return serr
	}

	// Seal the failed entry's Data too: the failed list retains payloads
	// indefinitely, so it must not become the plaintext copy of an
	// otherwise-encrypted queue (see queue/encryption.go).
	if err := queue.SealPayload(payload); err != nil {
		return err
	}

	// Add failure information
	failedData := map[string]interface{}{
		"payload":   payload,
		"error":     err.Error(),
		"failed_at": time.Now(),
	}

	data, merr := json.Marshal(failedData)
	if merr != nil {
		return fmt.Errorf("velocity/queue: failed to marshal failed job: %w", merr)
	}

	// Store in failed queue
	if pusherr := r.client.RPush(r.ctx, failedKey, data).Err(); pusherr != nil {
		return pusherr
	}

	// Call the job's Failed method
	job.Failed(err)

	return nil
}

// moveDelayedJobs moves ready delayed jobs to the main queue. The supplied
// ctx is honoured on every Redis round-trip so worker shutdown preempts the
// loop instead of waiting on the driver-lifetime r.ctx.
func (r *RedisDriver) moveDelayedJobs(ctx context.Context, queueName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	delayedKey := r.getDelayedKey(queueName)
	queueKey := r.getQueueKey(queueName)

	now := float64(time.Now().Unix())

	// Use ZPOPMIN to atomically get and remove ready jobs
	// This prevents multiple workers from processing the same delayed job
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Check if the minimum score is ready
		minScore, err := r.client.ZRangeWithScores(ctx, delayedKey, 0, 0).Result()
		if err != nil || len(minScore) == 0 {
			return nil // No delayed jobs
		}

		if minScore[0].Score > now {
			return nil // No jobs ready yet
		}

		// Atomically pop the job if it's still ready
		result, err := r.client.ZPopMin(ctx, delayedKey, 1).Result()
		if err != nil || len(result) == 0 {
			return nil
		}

		// The ZSET member should always be the JSON payload string we wrote
		// in PushDelayedCtx, but defend against go-redis returning an
		// unexpected type. Surface the bad entry via JobFailed so it isn't
		// silently dropped, then continue: ZPopMin already removed it from
		// the delayed set.
		member, ok := result[0].Member.(string)
		if !ok {
			queue.DispatchJobFailed(
				r.DispatchEvent,
				ctx,
				"unknown",
				queueName,
				fmt.Errorf("velocity/queue: delayed ZSET member has unexpected type %T", result[0].Member),
				0,
			)
			continue
		}

		if rpushErr := r.client.RPush(ctx, queueKey, member).Err(); rpushErr != nil {
			// The job is already gone from the delayed ZSET (ZPopMin
			// consumed it) but never made it to the main list. Recovery
			// must use a detached context with its own bounded budget:
			// the caller's ctx may be the cancelled worker shutdown ctx
			// that produced rpushErr in the first place, and reusing it
			// here would guarantee the recovery ZAdd fails too, silently
			// dropping the job. A fresh background ctx with a short
			// timeout gives recovery a chance even mid-shutdown.
			recoveryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			zaddErr := r.client.ZAdd(recoveryCtx, delayedKey, redis.Z{
				Score:  result[0].Score,
				Member: member,
			}).Err()
			cancel()
			if zaddErr != nil {
				return errors.Join(rpushErr, zaddErr)
			}
			return rpushErr
		}
	}
}

// Shutdown closes the Redis connection, honoring the context deadline.
//
// Idempotent per the contract.ShutdownAware contract: a second call after
// the underlying client is already closed returns nil rather than the
// "redis: client is closed" error the go-redis library raises on
// double-close. The provider registry and App.Shutdown invoke this in
// reverse order and may call it on retry paths; treating "already closed"
// as success keeps the contract clean.
func (r *RedisDriver) Shutdown(ctx context.Context) error {
	// The batch repository is process-wide (see queue/batch_repository.go)
	// and is owned by the app, not the queue driver. We no longer close
	// it here so sibling drivers (e.g. apps that fan out to multiple
	// Redis hosts) keep working after one driver shuts down.
	err := r.client.Close()
	if err != nil && errors.Is(err, redis.ErrClosed) {
		return nil
	}
	return err
}

// redisDedupePushScript is the Lua source for the at-most-once enqueue.
// It runs atomically inside Redis: if the dedupe sentinel already
// exists the script returns 0 without touching the queue list; if it
// does not, the script SETs the sentinel with the supplied TTL and
// RPUSHes the payload onto the queue list, returning 1.
//
// C-03 fb5: the previous implementation issued SETNX and RPUSH as two
// separate round trips with a DEL "rollback" on RPUSH failure. Two
// distinct failure modes leaked under that scheme:
//
//   - RPUSH lands on the server but the reply is lost (network reset
//     during the response, RTT spike, etc.). The Go client returns an
//     error; the rollback DEL removes the sentinel. The reaper's next
//     tick sees no sentinel and re-pushes. Duplicate callback.
//   - RPUSH does not land AND the rollback DEL fails (partitioned
//     server, client context cancelled). The sentinel persists for its
//     TTL while no queue entry exists, so the reaper's PushIfNotExists
//     no-ops forever. Callback lost until the TTL expires.
//
// C-03 fb6: Lua atomicity is NOT Redis transactionality. The SET, RPUSH
// pair runs without external interleaving but if RPUSH itself errors
// inside the script (WRONGTYPE because the queue key was reused by
// another caller as a string, OOM mid-script, eviction of the queue
// list under maxmemory pressure, and any future Redis-internal RPUSH
// error path we have not enumerated) the prior SET stands. The reaper's
// EXISTS branch then no-ops every subsequent retry until the 7d TTL
// expires; the callback is delayed up to 7 days or lost if the
// underlying cause is permanent. Wrapping RPUSH in `pcall` and DEL'ing
// the sentinel on failure makes the script transactional in the only
// failure mode that matters: if RPUSH errors for any Redis-level
// reason, the script clears the dedupe state in the same atomic
// execution and returns the original error so the caller can surface
// it. The next reaper tick retries cleanly because EXISTS is back to 0.
//
// `pcall` (vs. `call`) catches the Lua runtime error that the C-level
// command would otherwise propagate up the script, letting us run the
// DEL even though RPUSH "failed". `redis.error_reply` formats the
// returned error so go-redis surfaces it as an ordinary command error
// rather than a script-execution panic.
//
// KEYS[1] = dedupe sentinel key
// KEYS[2] = queue list key
// ARGV[1] = sentinel TTL seconds
// ARGV[2] = job payload (JSON bytes)
//
// Returns 1 when the script SET+RPUSH'd a new entry, 0 when the
// sentinel was already present, or a Redis error reply when RPUSH
// failed (in which case the sentinel was rolled back atomically).
const redisDedupePushScript = `
if redis.call('EXISTS', KEYS[1]) == 1 then
  return 0
end
redis.call('SET', KEYS[1], '1', 'EX', ARGV[1])
local ok, err = pcall(redis.call, 'RPUSH', KEYS[2], ARGV[2])
if not ok then
  redis.call('DEL', KEYS[1])
  return redis.error_reply(tostring(err))
end
return 1
`

// redisDedupePushScriptCompiled is the package-level *redis.Script for
// redisDedupePushScript. *redis.Script.Run() optimistically uses
// EVALSHA on its first invocation; if Redis replies NOSCRIPT it
// transparently falls back to EVAL, which both runs the script and
// caches its SHA1 server-side. Subsequent calls go through EVALSHA
// cleanly without reuploading the script body. Cached here so every
// driver instance shares one SHA1 hash and one upload.
var redisDedupePushScriptCompiled = redis.NewScript(redisDedupePushScript)

// PushIfNotExistsCtx implements DedupeAwarePusher via a single Lua
// script evaluated atomically on Redis. See redisDedupePushScript for
// the full failure analysis the script closes; in short, the SETNX +
// RPUSH + rollback-DEL sequence the previous implementation used had
// two partial-failure windows that could either drop the sentinel
// (allowing duplicates) or strand it (silently losing the callback).
//
// The sentinel TTL is 7 days, matching the job_batches prune horizon:
// the dedupe row outlives the typical callback-execution window so a
// stale reaper retry after the original queue entry was already
// consumed cannot re-enqueue it.
//
// Empty dedupeKey falls through to PushCtx for parity with the memory
// and database drivers; that is treated as a programmer error rather
// than a silent no-dedupe push.
func (r *RedisDriver) PushIfNotExistsCtx(ctx context.Context, job queue.Job, dedupeKey string, queueName ...string) error {
	if dedupeKey == "" {
		return r.PushCtx(ctx, job, queueName...)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	name := queue.ResolveQueueName(job, queueName...)
	queueKey := r.getQueueKey(name)
	sentinelKey := r.getDedupeKey(dedupeKey)

	payload, err := queue.SerializeJob(job, name)
	if err != nil {
		return err
	}
	payload.TraceID, payload.SpanID, payload.ParentID = trace.GetTraceContext(ctx)
	payload.DedupeKey = dedupeKey

	// Encrypt-then-sign: seal Data before marshalling so the signature
	// below covers the ciphertext (see queue/encryption.go).
	if err := queue.SealPayload(payload); err != nil {
		return err
	}

	data, err := queue.MarshalSigned(payload, func(sig string) { payload.Signature = sig },
		"velocity/queue: failed to marshal payload",
		"velocity/queue: failed to marshal signed payload")
	if err != nil {
		return err
	}

	const ttlSeconds = int64(7 * 24 * 60 * 60)
	// Run uses EVALSHA optimistically and falls back to EVAL on
	// NOSCRIPT, which keeps the redirection to Redis bounded to one
	// extra round trip on first-call-after-restart instead of every
	// invocation. Any transport error is bubbled up to the caller;
	// the reaper's next tick retries from the persisted intent.
	result, err := redisDedupePushScriptCompiled.Run(ctx,
		r.client,
		[]string{sentinelKey, queueKey},
		ttlSeconds, data,
	).Result()
	if err != nil {
		return fmt.Errorf("velocity/queue: redis dedupe push script: %w", err)
	}

	// The script returns an integer 0 or 1. go-redis types script
	// integer replies as int64; accept either int or int64 defensively.
	var pushed bool
	switch v := result.(type) {
	case int64:
		pushed = v == 1
	case int:
		pushed = v == 1
	default:
		return fmt.Errorf("velocity/queue: redis dedupe push script: unexpected reply type %T", result)
	}

	if !pushed {
		// Sentinel was already present; the duplicate dispatch is a
		// no-op success.
		return nil
	}

	queue.DispatchJobQueued(r.DispatchEvent, ctx, payload.Type, name, false, 0)
	return nil
}

// Helper methods
func (r *RedisDriver) getQueueKey(name string) string {
	return fmt.Sprintf("velocity:queue:%s", name)
}

func (r *RedisDriver) getDelayedKey(name string) string {
	return fmt.Sprintf("velocity:queue:%s:delayed", name)
}

func (r *RedisDriver) getFailedKey(name string) string {
	return fmt.Sprintf("velocity:queue:%s:failed", name)
}

// getDedupeKey returns the Redis key used as a SETNX sentinel for a
// given dedupe identifier. The prefix is shared with the queue keys
// (`velocity:queue:`) so an operator running `KEYS velocity:queue:*`
// sees both queue and dedupe state.
func (r *RedisDriver) getDedupeKey(dedupeKey string) string {
	return fmt.Sprintf("velocity:queue:dedupe:%s", dedupeKey)
}
