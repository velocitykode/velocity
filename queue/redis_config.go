package queue

import "fmt"

// RedisConfig holds Redis connection configuration.
//
// The type lives in the queue root (not the queue/redis leaf) because
// QueueConfig.Redis embeds it: keeping it here lets callers build a queue
// config without importing the heavy redis leaf, while the leaf driver
// consumes it via queue.RedisConfig. The Password field contains sensitive
// credentials and must not be logged.
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
