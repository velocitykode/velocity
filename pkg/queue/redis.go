package queue

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisConfig holds Redis connection configuration.
// The Password field contains sensitive credentials and must not be logged.
type RedisConfig struct {
	Host     string
	Port     string
	Password string // SENSITIVE: do not log
	DB       string
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
}

// NewRedisDriver creates a new Redis queue driver.
// Set REDIS_TLS=true environment variable to enable TLS connections.
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
	if os.Getenv("REDIS_TLS") == "true" {
		opts.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}

	client := redis.NewClient(opts)

	ctx := context.Background()

	// Test connection
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &RedisDriver{
		client: client,
		ctx:    ctx,
		config: config,
	}, nil
}

// Push adds a job to the queue
func (r *RedisDriver) Push(job Job, queueName ...string) error {
	name := r.getQueueName(queueName...)
	queueKey := r.getQueueKey(name)

	payload, err := SerializeJob(job, name)
	if err != nil {
		return err
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Sign the payload for integrity verification
	if sig := signPayload(data); sig != "" {
		payload.Signature = sig
		// Re-marshal with the signature included
		data, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal signed payload: %w", err)
		}
	}

	if err := r.client.RPush(r.ctx, queueKey, data).Err(); err != nil {
		return err
	}

	// Dispatch job.queued event
	dispatchJobQueued(r.ctx, payload.Type, name, false, 0)
	return nil
}

// PushDelayed adds a job to the queue with a delay
func (r *RedisDriver) PushDelayed(job Job, delay time.Duration, queueName ...string) error {
	name := r.getQueueName(queueName...)
	delayedKey := r.getDelayedKey(name)

	payload, err := SerializeJob(job, name)
	if err != nil {
		return err
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Sign the payload for integrity verification
	if sig := signPayload(data); sig != "" {
		payload.Signature = sig
		data, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal signed payload: %w", err)
		}
	}

	score := float64(time.Now().Add(delay).Unix())
	if err := r.client.ZAdd(r.ctx, delayedKey, redis.Z{
		Score:  score,
		Member: data,
	}).Err(); err != nil {
		return err
	}

	// Dispatch job.queued event with delay info
	dispatchJobQueued(r.ctx, payload.Type, name, true, delay)
	return nil
}

// Pop retrieves and removes the next job from the queue
func (r *RedisDriver) Pop(queueName string) (Job, error) {
	// First, move any ready delayed jobs to the main queue
	if err := r.moveDelayedJobs(queueName); err != nil {
		return nil, err
	}

	queueKey := r.getQueueKey(queueName)

	// Use BLPOP with a 1 second timeout for non-blocking behavior
	result, err := r.client.BLPop(r.ctx, 1*time.Second, queueKey).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil // No jobs available
		}
		return nil, err
	}

	if len(result) < 2 {
		return nil, nil
	}

	var payload Payload
	if err := json.Unmarshal([]byte(result[1]), &payload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal payload: %w", err)
	}

	// Verify payload integrity if signing is enabled
	sig := payload.Signature
	payload.Signature = "" // Remove signature before verification
	verifyData, _ := json.Marshal(payload)
	if err := verifyPayload(verifyData, sig); err != nil {
		return nil, fmt.Errorf("queue integrity check failed: %w", err)
	}

	// Deserialize the job using the registry
	job, err := registry.Deserialize(&payload)
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize job: %w", err)
	}
	return job, nil
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
		return fmt.Errorf("failed to marshal failed job: %w", merr)
	}

	// Store in failed queue
	if pusherr := r.client.RPush(r.ctx, failedKey, data).Err(); pusherr != nil {
		return pusherr
	}

	// Call the job's Failed method
	job.Failed(err)

	return nil
}

// moveDelayedJobs moves ready delayed jobs to the main queue
func (r *RedisDriver) moveDelayedJobs(queueName string) error {
	delayedKey := r.getDelayedKey(queueName)
	queueKey := r.getQueueKey(queueName)

	now := float64(time.Now().Unix())

	// Use ZPOPMIN to atomically get and remove ready jobs
	// This prevents multiple workers from processing the same delayed job
	for {
		// Check if the minimum score is ready
		minScore, err := r.client.ZRangeWithScores(r.ctx, delayedKey, 0, 0).Result()
		if err != nil || len(minScore) == 0 {
			return nil // No delayed jobs
		}

		if minScore[0].Score > now {
			return nil // No jobs ready yet
		}

		// Atomically pop the job if it's still ready
		result, err := r.client.ZPopMin(r.ctx, delayedKey, 1).Result()
		if err != nil || len(result) == 0 {
			return nil
		}

		// Push to main queue
		member := result[0].Member.(string)
		if err := r.client.RPush(r.ctx, queueKey, member).Err(); err != nil {
			// If push fails, put it back in delayed queue
			r.client.ZAdd(r.ctx, delayedKey, redis.Z{
				Score:  result[0].Score,
				Member: member,
			})
			return err
		}
	}
}

// Close closes the Redis connection
func (r *RedisDriver) Close() error {
	return r.client.Close()
}

// Helper methods
func (r *RedisDriver) getQueueName(queueName ...string) string {
	if len(queueName) > 0 && queueName[0] != "" {
		return queueName[0]
	}
	return "default"
}

func (r *RedisDriver) getQueueKey(name string) string {
	return fmt.Sprintf("velocity:queue:%s", name)
}

func (r *RedisDriver) getDelayedKey(name string) string {
	return fmt.Sprintf("velocity:queue:%s:delayed", name)
}

func (r *RedisDriver) getFailedKey(name string) string {
	return fmt.Sprintf("velocity:queue:%s:failed", name)
}
