package drivers

import (
	"context"
	"errors"
	"testing"
)

// TestSQLiteDriver_QueryContextCancellation verifies that cancelling the
// context passed to QueryContext propagates down to the driver so long-
// running queries abort promptly. We trigger cancellation before the
// query runs to exercise the fast path.
func TestSQLiteDriver_QueryContextCancellation(t *testing.T) {
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{
		Database:     ":memory:",
		MaxOpenConns: 1, // Keep the same connection so CREATE TABLE persists.
	}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer driver.Close()

	if _, err := driver.DB().Exec("CREATE TABLE ctx_test (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE the query — the driver should observe context.Canceled.

	_, err := driver.QueryContext(ctx, "SELECT * FROM ctx_test")
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestSQLiteDriver_ExecContextCancellation verifies ExecContext also
// honours context cancellation.
func TestSQLiteDriver_ExecContextCancellation(t *testing.T) {
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{
		Database:     ":memory:",
		MaxOpenConns: 1, // Keep the same connection so CREATE TABLE persists.
	}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer driver.Close()

	if _, err := driver.DB().Exec("CREATE TABLE ctx_exec (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := driver.ExecContext(ctx, "INSERT INTO ctx_exec (name) VALUES (?)", "alice")
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestSQLiteDriver_QueryRowContextCancellation verifies QueryRowContext
// surfaces cancellation via the returned row's Scan call.
func TestSQLiteDriver_QueryRowContextCancellation(t *testing.T) {
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{
		Database:     ":memory:",
		MaxOpenConns: 1, // Keep the same connection so CREATE TABLE persists.
	}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer driver.Close()

	if _, err := driver.DB().Exec("CREATE TABLE ctx_row (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var id int
	err := driver.QueryRowContext(ctx, "SELECT id FROM ctx_row LIMIT 1").Scan(&id)
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

