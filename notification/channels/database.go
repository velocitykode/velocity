package channels

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
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
//	  id              VARCHAR(36) PRIMARY KEY,
//	  type            VARCHAR(255) NOT NULL,
//	  notifiable_type VARCHAR(255) NOT NULL,
//	  notifiable_id   VARCHAR(255) NOT NULL,
//	  data            TEXT NOT NULL,
//	  read_at         TIMESTAMP NULL,
//	  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
//	  updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
//	);
//	CREATE INDEX idx_notifications_notifiable ON notifications (notifiable_type, notifiable_id);
//	CREATE INDEX idx_notifications_read_at ON notifications (read_at);
//
// id is a UUIDv4 string (RFC 4122), shared across every channel in the
// same Send call so the row inserted here correlates with the email,
// broadcast, etc. that went out in parallel.
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

	// Resolve polymorphic recipient type. The notification can declare
	// it explicitly via DatabaseMessage.WithNotifiableType, otherwise we
	// fall back to the Go runtime type so the column is always
	// populated (NOT NULL in the schema above).
	notifiableType := dbMsg.NotifiableType
	if notifiableType == "" {
		notifiableType = inferNotifiableType(notifiable)
	}

	// Serialize data to JSON
	dataJSON, err := json.Marshal(dbMsg.Data)
	if err != nil {
		return fmt.Errorf("notification: failed to serialize notification data: %w", err)
	}

	now := time.Now().UTC()

	// Re-use the per-Send notification ID propagated by Manager.Send so
	// the database row matches the same ID surfaced via mail headers,
	// broadcast payload, etc. Falls back to a fresh UUIDv4 when called
	// outside Manager.Send (e.g. direct ch.Send for tests).
	id := notification.IDFromContext(ctx)
	if id == "" {
		id = generateNotificationID()
	}

	query := rebind(c.driver,
		"INSERT INTO notifications (id, type, notifiable_type, notifiable_id, data, read_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, NULL, ?, ?)",
	)

	_, err = c.db.ExecContext(ctx, query,
		id, dbMsg.Type, notifiableType, notifiableID, string(dataJSON), now, now,
	)
	if err != nil {
		return fmt.Errorf("notification: failed to insert notification: %w", err)
	}

	return nil
}

// generateNotificationID generates a fresh RFC 4122 UUIDv4. Stable
// 36-char canonical form (8-4-4-4-12 hex with dashes). Replaces the
// previous ad-hoc 18-byte hex encoding which lacked the version /
// variant bits and could not be parsed by uuid-aware downstream tools.
func generateNotificationID() string {
	return uuid.NewString()
}

// inferNotifiableType derives a stable string for the notifiable's
// Go runtime type. Pointer wrappers are unwrapped so *models.User and
// models.User produce the same value; nil interfaces produce "".
func inferNotifiableType(notifiable interface{}) string {
	if notifiable == nil {
		return ""
	}
	t := reflect.TypeOf(notifiable)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return ""
	}
	return t.String()
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
