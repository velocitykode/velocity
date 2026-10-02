// Package contract stands in for the framework's contract package.
package contract

import (
	"context"
	"time"
)

// EventMeta is the envelope every framework event embeds.
type EventMeta struct {
	Context context.Context `json:"-"`
	TraceID string
	At      time.Time
}

// Logger stands in for the framework logger.
type Logger interface{ Warn(msg string, kvs ...any) }

// QueueJob stands in for the framework's queue job.
type QueueJob interface {
	Handle() error
	Failed(error)
}
