package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/queue"
)

type redisAttemptsJob struct {
	ID string `json:"id"`
}

func (j *redisAttemptsJob) Handle() error    { return nil }
func (j *redisAttemptsJob) Failed(error)     {}
func (j *redisAttemptsJob) MaxAttempts() int { return 3 }

func init() {
	queue.RegisterJob(func(data []byte) (*redisAttemptsJob, error) {
		var job redisAttemptsJob
		if err := json.Unmarshal(data, &job); err != nil {
			return nil, err
		}
		return &job, nil
	})
}

type redisCaptureLogger struct {
	mu       sync.Mutex
	messages []string
}

func (l *redisCaptureLogger) Info(string, ...any)  {}
func (l *redisCaptureLogger) Error(string, ...any) {}

func (l *redisCaptureLogger) Warn(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, msg)
}

func (l *redisCaptureLogger) countContaining(needle string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	var n int
	for _, msg := range l.messages {
		if strings.Contains(msg, needle) {
			n++
		}
	}
	return n
}

func redisDelayedPayload(t *testing.T, driver *RedisDriver, queueName string) queue.Payload {
	t.Helper()
	members, err := driver.client.ZRange(context.Background(), driver.getDelayedKey(queueName), 0, -1).Result()
	if err != nil {
		t.Fatalf("read delayed payloads: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("delayed payload count = %d, want 1", len(members))
	}
	var payload queue.Payload
	if err := json.Unmarshal([]byte(members[0]), &payload); err != nil {
		t.Fatalf("unmarshal delayed payload: %v", err)
	}
	return payload
}

func redisPoppedAttemptsLen(driver *RedisDriver) int {
	var n int
	driver.poppedAttempts.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}

func TestRedisDriver_NonIdentifiableRequeuePersistsAttemptsAndWarnsOnce(t *testing.T) {
	saveAndRestoreSigningState(t)
	queue.SetSigningKey(nil)

	tests := []struct {
		name string
	}{
		{name: "non-identifiable job"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver, _ := newMiniRedisDriver(t)
			logger := &redisCaptureLogger{}
			driver.SetLogger(logger)

			queueName := "redis-attempts-persist"
			if err := driver.PushCtx(context.Background(), &redisAttemptsJob{ID: "job-1"}, queueName); err != nil {
				t.Fatalf("initial push: %v", err)
			}

			job1, _, err := driver.PopCtxWithTrace(context.Background(), queueName)
			if err != nil {
				t.Fatalf("first pop: %v", err)
			}
			if _, ok := job1.(*redisAttemptsJob); !ok {
				t.Fatalf("first pop returned %T, want *redisAttemptsJob", job1)
			}

			if err := driver.PushDelayedCtx(context.Background(), job1, -time.Second, queueName); err != nil {
				t.Fatalf("first requeue: %v", err)
			}
			if payload := redisDelayedPayload(t, driver, queueName); payload.Attempts != 1 {
				t.Fatalf("first requeue persisted Attempts=%d, want 1", payload.Attempts)
			}

			job2, _, err := driver.PopCtxWithTrace(context.Background(), queueName)
			if err != nil {
				t.Fatalf("second pop: %v", err)
			}
			if _, ok := job2.(*redisAttemptsJob); !ok {
				t.Fatalf("second pop returned %T, want *redisAttemptsJob", job2)
			}

			if err := driver.PushDelayedCtx(context.Background(), job2, -time.Second, queueName); err != nil {
				t.Fatalf("second requeue: %v", err)
			}
			if payload := redisDelayedPayload(t, driver, queueName); payload.Attempts != 2 {
				t.Fatalf("second requeue persisted Attempts=%d, want 2", payload.Attempts)
			}

			if got := logger.countContaining("does not implement Identifiable"); got != 1 {
				t.Fatalf("Identifiable advisory count = %d, want 1", got)
			}
		})
	}
}

func TestRedisDriver_PoppedAttemptsCacheIsBounded(t *testing.T) {
	saveAndRestoreSigningState(t)
	queue.SetSigningKey(nil)

	driver, _ := newMiniRedisDriver(t)
	queueName := "redis-attempts-bounded"

	for i := 0; i < redisPoppedAttemptsMaxEntries+10; i++ {
		if err := driver.PushCtx(context.Background(), &redisAttemptsJob{ID: fmt.Sprintf("job-%d", i)}, queueName); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}

	for i := 0; i < redisPoppedAttemptsMaxEntries+10; i++ {
		job, _, err := driver.PopCtxWithTrace(context.Background(), queueName)
		if err != nil {
			t.Fatalf("pop %d: %v", i, err)
		}
		if job == nil {
			t.Fatalf("pop %d: got nil job", i)
		}
	}

	if got := redisPoppedAttemptsLen(driver); got > redisPoppedAttemptsMaxEntries {
		t.Fatalf("poppedAttempts len = %d, want <= %d", got, redisPoppedAttemptsMaxEntries)
	}
}
