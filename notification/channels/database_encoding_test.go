package channels

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/notification"
)

// htmlNotification carries HTML metacharacters in its Data so we can
// pin that the database channel stores the raw UTF-8 bytes rather
// than json.Marshal's default <,>,& escapes.
type htmlNotification struct{}

func (htmlNotification) Via(_ interface{}) []string { return []string{"database"} }
func (htmlNotification) ToDatabase(_ interface{}) *notification.DatabaseMessage {
	return notification.NewDatabaseMessage("html.subject").
		Set("body", "<b>hi</b> & welcome")
}

// TestDatabaseChannel_StoresRawHTMLChars pins that '<', '>', '&' in
// Data values survive into the stored column as raw UTF-8 bytes.
// Without SetEscapeHTML(false), json.Marshal's default would encode
// these as <, >, & which corrupt every downstream
// consumer that does not re-JSON-decode the column before render.
func TestDatabaseChannel_StoresRawHTMLChars(t *testing.T) {
	db := newSQLiteDB(t)
	ch := NewDatabaseChannel()
	ch.SetDB(db, "sqlite")

	notifiable := &databaseTestNotifiable{id: "42"}
	if err := ch.Send(context.Background(), notifiable, htmlNotification{}); err != nil {
		t.Fatalf("send: %v", err)
	}

	var got string
	if err := db.QueryRow("SELECT data FROM notifications LIMIT 1").Scan(&got); err != nil {
		t.Fatalf("scan data: %v", err)
	}
	// Positive: raw bytes survive.
	if !strings.Contains(got, "<b>hi</b> & welcome") {
		t.Errorf("stored data %q does not contain raw HTML chars", got)
	}
	// Negative: default JSON escape sequences must NOT appear.
	for _, esc := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(got, esc) {
			t.Errorf("stored data %q contains default JSON escape %q", got, esc)
		}
	}
}

// poisonNotification carries a NaN in its Data so we can pin that the
// channel surfaces a clean serialization error rather than panicking
// or writing a corrupted row.
type poisonNotification struct{}

func (poisonNotification) Via(_ interface{}) []string { return []string{"database"} }
func (poisonNotification) ToDatabase(_ interface{}) *notification.DatabaseMessage {
	return notification.NewDatabaseMessage("poison").Set("ratio", math.NaN())
}

// TestDatabaseChannel_NonEncodableValueReturnsError pins that a
// non-encodable value (NaN) surfaces a serialize error from Send
// rather than corrupting the row or panicking.
func TestDatabaseChannel_NonEncodableValueReturnsError(t *testing.T) {
	db := newSQLiteDB(t)
	ch := NewDatabaseChannel()
	ch.SetDB(db, "sqlite")

	notifiable := &databaseTestNotifiable{id: "42"}
	err := ch.Send(context.Background(), notifiable, poisonNotification{})
	if err == nil {
		t.Fatal("expected serialize error for NaN value, got nil")
	}
	if !strings.Contains(err.Error(), "serialize notification data") {
		t.Errorf("error %q does not mention serialize failure", err.Error())
	}

	// And nothing was inserted into the table.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM notifications").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows after failed Send, got %d", count)
	}
}
