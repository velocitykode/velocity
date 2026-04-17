package storage

import (
	"context"
	"time"
)

// StorageOperationFailed is dispatched when a storage operation fails
type StorageOperationFailed struct {
	Context context.Context
	Disk    string
	Op      string // "put", "get", "delete", "exists"
	Path    string
	Error   string
	At      time.Time
}

// Name returns the event name
func (e *StorageOperationFailed) Name() string {
	return "storage.operation.failed"
}
