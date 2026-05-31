package database

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	_ "github.com/mattn/go-sqlite3"

	"github.com/velocitykode/velocity/notification"
)

// databaseTestNotifiable carries a database route and a recognisable
// runtime type so inferNotifiableType produces a stable string.
type databaseTestNotifiable struct {
	id string
}

func (n *databaseTestNotifiable) NotificationRoute(channel string) string {
	if channel == "database" {
		return n.id
	}
	return ""
}

type dbNotification struct {
	subject string
	id      string
}

func (n *dbNotification) Via(_ interface{}) []string { return []string{"database"} }

func (n *dbNotification) ToDatabase(_ interface{}) *notification.DatabaseMessage {
	return notification.NewDatabaseMessage("order.shipped").Set("subject", n.subject)
}

// Optional ID provider: notifications that want to anchor the ID to
// their own state (e.g. a DB primary key) opt in via this method.
func (n *dbNotification) ID() string { return n.id }

func newSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE notifications (
		id              TEXT PRIMARY KEY,
		type            TEXT NOT NULL,
		notifiable_type TEXT NOT NULL,
		notifiable_id   TEXT NOT NULL,
		data            TEXT NOT NULL,
		read_at         TIMESTAMP NULL,
		created_at      TIMESTAMP NOT NULL,
		updated_at      TIMESTAMP NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

func TestDatabaseChannel_GeneratedIDIsValidUUID(t *testing.T) {
	db := newSQLiteDB(t)
	ch := NewDatabaseChannel()
	ch.SetDB(db, "sqlite")

	notifiable := &databaseTestNotifiable{id: "42"}
	// Notification without an explicit ID() must yield a manager-generated UUID.
	n := &dbNotification{subject: "Welcome"} // implements WithID but with empty ID
	n.id = ""                                // empty -> fallback to UUID

	if err := ch.Send(context.Background(), notifiable, n); err != nil {
		t.Fatalf("send: %v", err)
	}

	var id string
	row := db.QueryRow("SELECT id FROM notifications LIMIT 1")
	if err := row.Scan(&id); err != nil {
		t.Fatalf("scan id: %v", err)
	}
	if _, err := uuid.Parse(id); err != nil {
		t.Errorf("expected RFC4122 UUID, got %q: %v", id, err)
	}
}

func TestDatabaseChannel_NotifiableTypePopulated(t *testing.T) {
	db := newSQLiteDB(t)
	ch := NewDatabaseChannel()
	ch.SetDB(db, "sqlite")

	notifiable := &databaseTestNotifiable{id: "42"}
	n := &dbNotification{subject: "Welcome"}

	if err := ch.Send(context.Background(), notifiable, n); err != nil {
		t.Fatalf("send: %v", err)
	}

	var notifiableType string
	row := db.QueryRow("SELECT notifiable_type FROM notifications LIMIT 1")
	if err := row.Scan(&notifiableType); err != nil {
		t.Fatalf("scan notifiable_type: %v", err)
	}
	// inferNotifiableType unwraps pointers, so we expect "database.databaseTestNotifiable".
	if notifiableType != "database.databaseTestNotifiable" {
		t.Errorf("notifiable_type = %q, want %q", notifiableType, "database.databaseTestNotifiable")
	}
}

func TestDatabaseChannel_ExplicitNotifiableTypeOverridesRuntime(t *testing.T) {
	db := newSQLiteDB(t)
	ch := NewDatabaseChannel()
	ch.SetDB(db, "sqlite")

	notifiable := &databaseTestNotifiable{id: "42"}
	n := &explicitTypeNotification{}

	if err := ch.Send(context.Background(), notifiable, n); err != nil {
		t.Fatalf("send: %v", err)
	}

	var got string
	if err := db.QueryRow("SELECT notifiable_type FROM notifications LIMIT 1").Scan(&got); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got != "App.Models.User" {
		t.Errorf("notifiable_type = %q, want %q", got, "App.Models.User")
	}
}

type explicitTypeNotification struct{}

func (explicitTypeNotification) Via(_ interface{}) []string { return []string{"database"} }

func (explicitTypeNotification) ToDatabase(_ interface{}) *notification.DatabaseMessage {
	return notification.NewDatabaseMessage("user.welcomed").WithNotifiableType("App.Models.User")
}

// captureChannel records the ID it observed on the per-Send context so a
// test can assert that database + broadcast saw the same string.
type captureChannel struct {
	mu       sync.Mutex
	seen     []string
	delegate func()
}

func (c *captureChannel) Send(ctx context.Context, _ interface{}, _ notification.Notification) error {
	c.mu.Lock()
	c.seen = append(c.seen, notification.IDFromContext(ctx))
	c.mu.Unlock()
	if c.delegate != nil {
		c.delegate()
	}
	return nil
}

func (c *captureChannel) ids() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.seen))
	copy(out, c.seen)
	return out
}

// multiVia routes through several channels in one Send.
type multiVia struct {
	channels []string
}

func (n *multiVia) Via(_ interface{}) []string { return n.channels }
func (n *multiVia) ToDatabase(_ interface{}) *notification.DatabaseMessage {
	return notification.NewDatabaseMessage("multi")
}

func TestNotification_IDSharedAcrossChannelsInOneSend(t *testing.T) {
	mgr := notification.NewManager()

	a := &captureChannel{}
	b := &captureChannel{}
	mgr.SetChannel("a", a)
	mgr.SetChannel("b", b)

	notifiable := &databaseTestNotifiable{id: "42"}
	n := &multiVia{channels: []string{"a", "b"}}

	if err := mgr.Send(context.Background(), notifiable, n); err != nil {
		t.Fatalf("send: %v", err)
	}

	aIDs := a.ids()
	bIDs := b.ids()
	if len(aIDs) != 1 || len(bIDs) != 1 {
		t.Fatalf("expected 1 send per channel; got a=%v b=%v", aIDs, bIDs)
	}
	if aIDs[0] == "" {
		t.Fatal("channel a saw no notification ID")
	}
	if aIDs[0] != bIDs[0] {
		t.Errorf("channels saw different IDs: a=%q b=%q", aIDs[0], bIDs[0])
	}
	if _, err := uuid.Parse(aIDs[0]); err != nil {
		t.Errorf("shared notification ID must be a UUID, got %q: %v", aIDs[0], err)
	}
}

// withIDNotification implements WithID to pin the per-Send identifier
// to an application-controlled string.
type withIDNotification struct {
	id       string
	channels []string
}

func (n *withIDNotification) Via(_ interface{}) []string { return n.channels }
func (n *withIDNotification) ID() string                 { return n.id }
func (n *withIDNotification) ToDatabase(_ interface{}) *notification.DatabaseMessage {
	return notification.NewDatabaseMessage("with-id")
}

func TestNotification_WithIDOverridesGeneratedID(t *testing.T) {
	mgr := notification.NewManager()
	a := &captureChannel{}
	mgr.SetChannel("a", a)

	const want = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	n := &withIDNotification{id: want, channels: []string{"a"}}

	if err := mgr.Send(context.Background(), &databaseTestNotifiable{id: "42"}, n); err != nil {
		t.Fatalf("send: %v", err)
	}
	ids := a.ids()
	if len(ids) != 1 || ids[0] != want {
		t.Errorf("expected WithID to override; got %v", ids)
	}
}

func TestNotification_IDFromContextEmptyByDefault(t *testing.T) {
	if got := notification.IDFromContext(context.Background()); got != "" {
		t.Errorf("expected empty default ID, got %q", got)
	}
}

func TestNotification_WithNotificationIDIsReadBack(t *testing.T) {
	ctx := notification.WithNotificationID(context.Background(), "the-id")
	if got := notification.IDFromContext(ctx); got != "the-id" {
		t.Errorf("WithNotificationID/IDFromContext round-trip = %q, want %q", got, "the-id")
	}
}

// TestDatabaseChannel_IDColumnMatchesContextID exercises the contract
// between Manager.Send (allocates the per-Send ID) and DatabaseChannel
// (reads it from context for the inserted row). Without this wiring,
// a notification fired through mail+database produces two unrelated IDs
// and the original M-26 confusion returns.
func TestDatabaseChannel_IDColumnMatchesContextID(t *testing.T) {
	db := newSQLiteDB(t)
	ch := NewDatabaseChannel()
	ch.SetDB(db, "sqlite")

	const ctxID = "11111111-2222-3333-4444-555555555555"
	ctx := notification.WithNotificationID(context.Background(), ctxID)

	notifiable := &databaseTestNotifiable{id: "1"}
	n := &dbNotification{subject: "ctx", id: ""}

	if err := ch.Send(ctx, notifiable, n); err != nil {
		t.Fatalf("send: %v", err)
	}
	var got string
	if err := db.QueryRow("SELECT id FROM notifications").Scan(&got); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got != ctxID {
		t.Errorf("db id = %q, want context id %q", got, ctxID)
	}
}

// TestDatabaseChannel_FallbackIDWhenCalledWithoutManager exercises the
// "direct ch.Send for tests" path; channels still get a parseable UUID
// when called outside Manager.Send.
func TestDatabaseChannel_FallbackIDWhenCalledWithoutManager(t *testing.T) {
	db := newSQLiteDB(t)
	ch := NewDatabaseChannel()
	ch.SetDB(db, "sqlite")

	if err := ch.Send(context.Background(), &databaseTestNotifiable{id: "1"}, &dbNotification{subject: "x"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	var got string
	if err := db.QueryRow("SELECT id FROM notifications").Scan(&got); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := uuid.Parse(got); err != nil {
		t.Errorf("fallback id %q is not a UUID: %v", got, err)
	}
}

func TestInferNotifiableType_UnwrapsPointer(t *testing.T) {
	type someUser struct{}
	got := inferNotifiableType(&someUser{})
	if got != "database.someUser" {
		t.Errorf("expected pointer to unwrap to database.someUser, got %q", got)
	}
}

func TestInferNotifiableType_NilInterface(t *testing.T) {
	if got := inferNotifiableType(nil); got != "" {
		t.Errorf("expected empty for nil, got %q", got)
	}
}

// Sanity: NewID always parses as UUID and never collides across many calls.
func TestNotification_NewIDUniquenessAndFormat(t *testing.T) {
	seen := make(map[string]struct{}, 4096)
	var wg sync.WaitGroup
	var failures atomic.Int64
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 512; j++ {
				id := notification.NewID()
				if _, err := uuid.Parse(id); err != nil {
					failures.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("NewID produced %d invalid UUIDs", failures.Load())
	}
	for i := 0; i < 4096; i++ {
		id := notification.NewID()
		if _, ok := seen[id]; ok {
			t.Fatalf("collision at iteration %d: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}
