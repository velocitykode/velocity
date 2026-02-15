package channels

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/velocitykode/velocity/notification"
)

func init() {
	notification.RegisterChannel("database", func() (notification.Channel, error) {
		return NewDatabaseChannel(), nil
	})
}

// DatabaseChannel stores notifications in a database table.
// The table schema is:
//
//	CREATE TABLE notifications (
//	  id            VARCHAR(36) PRIMARY KEY,
//	  type          VARCHAR(255) NOT NULL,
//	  notifiable_id VARCHAR(255) NOT NULL,
//	  data          TEXT NOT NULL,
//	  read_at       TIMESTAMP NULL,
//	  created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
//	  updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
//	);
type DatabaseChannel struct {
	db *sql.DB
}

// NewDatabaseChannel creates a new database notification channel.
func NewDatabaseChannel() *DatabaseChannel {
	return &DatabaseChannel{}
}

// SetDB sets the database connection used to store notifications.
func (c *DatabaseChannel) SetDB(db *sql.DB) {
	c.db = db
}

// Send stores a notification in the database.
func (c *DatabaseChannel) Send(ctx context.Context, notifiable interface{}, n notification.Notification) error {
	dn, ok := n.(notification.DatabaseNotification)
	if !ok {
		return fmt.Errorf("notification: %T does not implement DatabaseNotification", n)
	}

	dbMsg := dn.ToDatabase(notifiable)
	if dbMsg == nil {
		return nil
	}

	if c.db == nil {
		return fmt.Errorf("notification: database channel has no database connection configured")
	}

	// Get the notifiable ID
	notifiableID := ""
	if nr, ok := notifiable.(notification.Notifiable); ok {
		notifiableID = nr.NotificationRoute("database")
	}

	// Serialize data to JSON
	dataJSON, err := json.Marshal(dbMsg.Data)
	if err != nil {
		return fmt.Errorf("notification: failed to serialize notification data: %w", err)
	}

	now := time.Now().UTC()

	// Use a UUID-like ID based on timestamp and random bytes
	id := generateNotificationID()

	_, err = c.db.ExecContext(ctx,
		"INSERT INTO notifications (id, type, notifiable_id, data, read_at, created_at, updated_at) VALUES ($1, $2, $3, $4, NULL, $5, $6)",
		id, dbMsg.Type, notifiableID, string(dataJSON), now, now,
	)
	if err != nil {
		return fmt.Errorf("notification: failed to insert notification: %w", err)
	}

	return nil
}

// generateNotificationID generates a simple unique ID for a notification record.
func generateNotificationID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
