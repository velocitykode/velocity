package channels

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/velocitykode/velocity/notification"
)

func init() {
	notification.Drivers().Register("database", func(_ context.Context, _ notification.ChannelConfig) (notification.Channel, error) {
		return NewDatabaseChannel(), nil
	})
}

// DatabaseChannel stores notifications in a database table.
//
// Expected table schema (create via a migration):
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
//	CREATE INDEX idx_notifications_notifiable ON notifications (notifiable_id);
//	CREATE INDEX idx_notifications_read_at ON notifications (read_at);
type DatabaseChannel struct {
	db     *sql.DB
	driver string // "postgres", "mysql", or "sqlite"
}

// NewDatabaseChannel creates a new database notification channel.
func NewDatabaseChannel() *DatabaseChannel {
	return &DatabaseChannel{}
}

// SetDB sets the database connection and driver name used to store notifications.
// The driver name determines placeholder syntax ("postgres" uses $1, others use ?).
func (c *DatabaseChannel) SetDB(db *sql.DB, driver ...string) {
	c.db = db
	if len(driver) > 0 {
		c.driver = driver[0]
	}
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
	id := generateNotificationID()

	query := rebind(c.driver,
		"INSERT INTO notifications (id, type, notifiable_id, data, read_at, created_at, updated_at) VALUES (?, ?, ?, ?, NULL, ?, ?)",
	)

	_, err = c.db.ExecContext(ctx, query,
		id, dbMsg.Type, notifiableID, string(dataJSON), now, now,
	)
	if err != nil {
		return fmt.Errorf("notification: failed to insert notification: %w", err)
	}

	return nil
}

// generateNotificationID generates a cryptographically random 36-character hex ID.
func generateNotificationID() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		// Fallback: include timestamp to reduce collision risk
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// rebind converts ? placeholders to $1, $2, ... for PostgreSQL.
// For all other drivers the query is returned unchanged.
func rebind(driver, query string) string {
	if driver != "postgres" {
		return query
	}
	var buf strings.Builder
	idx := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			buf.WriteString(fmt.Sprintf("$%d", idx))
			idx++
		} else {
			buf.WriteByte(query[i])
		}
	}
	return buf.String()
}
