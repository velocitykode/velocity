package queue

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/velocitykode/velocity/trace"
)

// RedisConfig holds Redis connection configuration.
// The Password field contains sensitive credentials and must not be logged.
type RedisConfig struct {
	Host     string
	Port     string
	Password string // SENSITIVE: do not log
	DB       string
	TLS      bool // Enable TLS connections
}

// String returns a safe representation with credentials redacted.
func (c RedisConfig) String() string {
	return fmt.Sprintf("RedisConfig{Host:%s, Port:%s, DB:%s, Password:[REDACTED]}", c.Host, c.Port, c.DB)
}

// RedisDriver implements Queue interface using Redis
type RedisDriver struct {
	client *redis.Client
	ctx    context.Context
	config RedisConfig
	// eventDispatcher is stored via atomic.Pointer so the dispatcher path
	// never acquires a lock. This keeps SetEventDispatcher safe to call
	// from any context (including callers that may already hold other
	// locks) and avoids a self-deadlock against future critical sections
	// that wrap PushCtx/PopCtx.
	eventDispatcher atomic.Pointer[dispatcherFn]
}

// NewRedisDriver creates a new Redis queue driver.
func NewRedisDriver(config RedisConfig) (*RedisDriver, error) {
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

	return &RedisDriver{
		client: client,
		ctx:    ctx,
		config: config,
	}, nil
}

// SetEventDispatcher installs the event dispatcher. The assignment goes
// through atomic.Pointer and never touches any lock, so it is safe to call
// from inside callers that already hold unrelated mutexes.
func (r *RedisDriver) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	if fn == nil {
		r.eventDispatcher.Store(nil)
		return
	}
	f := dispatcherFn(fn)
	r.eventDispatcher.Store(&f)
}

// dispatchEvent dispatches an event if a dispatcher is configured. The
// caller-supplied ctx is propagated so listeners observe request-scoped
// values. The dispatcher pointer is loaded atomically and invoked outside
// any lock.
func (r *RedisDriver) dispatchEvent(ctx context.Context, event interface{}) {
	p := r.eventDispatcher.Load()
	if p == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	(*p)(ctx, event)
}

// PushCtx adds a job to the queue using the caller's context.
func (r *RedisDriver) PushCtx(ctx context.Context, job Job, queueName ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := resolveQueueName(job, queueName...)
	queueKey := r.getQueueKey(name)

	payload, err := SerializeJob(job, name)
	if err != nil {
		return err
	}
	payload.TraceID, payload.SpanID, payload.ParentID = trace.GetTraceContext(ctx)

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("velocity/queue: failed to marshal payload: %w", err)
	}

	if sig := signPayload(data); sig != "" {
		payload.Signature = sig
		data, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("velocity/queue: failed to marshal signed payload: %w", err)
		}
	}

	if err := r.client.RPush(ctx, queueKey, data).Err(); err != nil {
		return err
	}

	dispatchJobQueued(r.dispatchEvent, ctx, payload.Type, name, false, 0)
	return nil
}

// PushDelayedCtx adds a delayed job using the caller's context.
func (r *RedisDriver) PushDelayedCtx(ctx context.Context, job Job, delay time.Duration, queueName ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := resolveQueueName(job, queueName...)
	delayedKey := r.getDelayedKey(name)

	payload, err := SerializeJob(job, name)
	if err != nil {
		return err
	}
	payload.TraceID, payload.SpanID, payload.ParentID = trace.GetTraceContext(ctx)

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("velocity/queue: failed to marshal payload: %w", err)
	}

	if sig := signPayload(data); sig != "" {
		payload.Signature = sig
		data, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("velocity/queue: failed to marshal signed payload: %w", err)
		}
	}

	score := float64(time.Now().Add(delay).Unix())
	if err := r.client.ZAdd(ctx, delayedKey, redis.Z{
		Score:  score,
		Member: data,
	}).Err(); err != nil {
		return err
	}

	dispatchJobQueued(r.dispatchEvent, ctx, payload.Type, name, true, delay)
	return nil
}

// PopCtx retrieves and removes the next job, honouring ctx cancellation on
// the BLPOP round-trip so worker shutdown aborts without waiting the full
// BLPOP timeout.
func (r *RedisDriver) PopCtx(ctx context.Context, queueName string) (Job, error) {
	job, _, err := r.PopCtxWithTrace(ctx, queueName)
	return job, err
}

// PopCtxWithTrace returns the popped job along with the producer-side trace
// context recovered from the persisted payload. Implements TraceAwareDriver.
func (r *RedisDriver) PopCtxWithTrace(ctx context.Context, queueName string) (Job, TraceContext, error) {
	var tc TraceContext
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

	var payload Payload
	if err := json.Unmarshal([]byte(result[1]), &payload); err != nil {
		return nil, tc, fmt.Errorf("velocity/queue: failed to unmarshal payload: %w", err)
	}

	// Verify payload integrity if signing is enabled.
	sig := payload.Signature
	payload.Signature = "" // Remove signature before verification
	verifyData, err := json.Marshal(payload)
	if err != nil {
		return nil, tc, fmt.Errorf("velocity/queue: failed to marshal payload for verification: %w", err)
	}
	if err := verifyPayload(verifyData, sig); err != nil {
		return nil, tc, fmt.Errorf("velocity/queue: queue integrity check failed: %w", err)
	}

	tc = TraceContext{TraceID: payload.TraceID, SpanID: payload.SpanID, ParentID: payload.ParentID}

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
	job, err := registry.Deserialize(&payload)
	if err != nil {
		return nil, tc, fmt.Errorf("velocity/queue: failed to deserialize job: %w", err)
	}
	return job, tc, nil
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

// Clear removes all jobs from the queue
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
func (r *RedisDriver) Failed(job Job, err error, queueName string) error {
	failedKey := r.getFailedKey(queueName)

	payload, serr := SerializeJob(job, queueName)
	if serr != nil {
		return serr
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
			dispatchJobFailed(
				r.dispatchEvent,
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
func (r *RedisDriver) Shutdown(ctx context.Context) error {
	// The batch repository is process-wide (see queue/batch_repository.go)
	// and is owned by the app, not the queue driver. We no longer close
	// it here so sibling drivers (e.g. apps that fan out to multiple
	// Redis hosts) keep working after one driver shuts down.
	return r.client.Close()
}

// PushIfNotExistsCtx implements DedupeAwarePusher using a sentinel
// SET ... NX EX on a key derived from the dedupe identifier. When the
// SETNX succeeds the job is RPush'd onto the queue list; when it fails
// (key already present) the function returns nil without queueing.
//
// The sentinel TTL is intentionally long (24h) so a callback whose
// queue entry is sitting in the list for hours under a backlog is
// still protected from a reaper double-push. After the worker pops
// the job and calls back into the driver (via the Popping deletion
// path), the sentinel is explicitly DEL'd so a legitimate second
// dispatch (different batch, same kind) is not blocked.
//
// Empty dedupeKey falls through to PushCtx for parity with the memory
// driver. This is treated as a programmer error rather than a silent
// no-dedupe push.
func (r *RedisDriver) PushIfNotExistsCtx(ctx context.Context, job Job, dedupeKey string, queueName ...string) error {
	if dedupeKey == "" {
		return r.PushCtx(ctx, job, queueName...)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	name := resolveQueueName(job, queueName...)
	queueKey := r.getQueueKey(name)
	sentinelKey := r.getDedupeKey(dedupeKey)

	// SETNX with a 7d TTL: holds the dedupe sentinel past the typical
	// callback-execution window so a stale reaper re-push (after the
	// original job was consumed but MarkCallbackDispatched failed) is
	// idempotent. 7 days matches the job_batches prune horizon.
	ok, err := r.client.SetNX(ctx, sentinelKey, "1", 7*24*time.Hour).Result()
	if err != nil {
		return fmt.Errorf("velocity/queue: redis SETNX dedupe: %w", err)
	}
	if !ok {
		// Already enqueued; treat as success.
		return nil
	}

	payload, err := SerializeJob(job, name)
	if err != nil {
		// Roll back the sentinel so a retry can succeed.
		_ = r.client.Del(ctx, sentinelKey).Err()
		return err
	}
	payload.TraceID, payload.SpanID, payload.ParentID = trace.GetTraceContext(ctx)
	payload.DedupeKey = dedupeKey

	data, err := json.Marshal(payload)
	if err != nil {
		_ = r.client.Del(ctx, sentinelKey).Err()
		return fmt.Errorf("velocity/queue: failed to marshal payload: %w", err)
	}
	if sig := signPayload(data); sig != "" {
		payload.Signature = sig
		data, err = json.Marshal(payload)
		if err != nil {
			_ = r.client.Del(ctx, sentinelKey).Err()
			return fmt.Errorf("velocity/queue: failed to marshal signed payload: %w", err)
		}
	}

	if err := r.client.RPush(ctx, queueKey, data).Err(); err != nil {
		// RPush failed: drop the sentinel so the caller can retry.
		// Without this rollback the SETNX would block all subsequent
		// retries for the 24h TTL.
		_ = r.client.Del(ctx, sentinelKey).Err()
		return err
	}

	dispatchJobQueued(r.dispatchEvent, ctx, payload.Type, name, false, 0)
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
