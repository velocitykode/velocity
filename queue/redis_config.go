package queue

import "fmt"

// RedisConfig holds Redis connection configuration.
//
// The type lives in the queue root (not the queue/redis leaf) because
// Config.Redis embeds it: keeping it here lets callers build a queue
// config without importing the heavy redis leaf, while the leaf driver
// consumes it via queue.RedisConfig. The Password field contains sensitive
// credentials and must not be logged.
type RedisConfig struct {
	// DB is the Redis database number (QUEUE_REDIS_DB, default "0").
	DB string

	// Host is the Redis server host (QUEUE_REDIS_HOST, default
	// "localhost").
	Host string

	// Password is the Redis password. SENSITIVE: do not log
	// (QUEUE_REDIS_PASSWORD, default empty).
	Password string

	// Port is the Redis server port (QUEUE_REDIS_PORT, default "6379").
	Port string

	// TLS enables TLS connections (REDIS_TLS, default false).
	TLS bool
}

// String returns a safe representation with credentials redacted.
func (c RedisConfig) String() string {
	return fmt.Sprintf("RedisConfig{Host:%s, Port:%s, DB:%s, Password:[REDACTED]}", c.Host, c.Port, c.DB)
}
