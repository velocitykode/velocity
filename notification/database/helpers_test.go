package database

import (
	"testing"

	"github.com/google/uuid"
)

func TestRebindPostgres(t *testing.T) {
	query := "INSERT INTO t (a, b, c) VALUES (?, ?, ?)"
	result := rebind("postgres", query)
	expected := "INSERT INTO t (a, b, c) VALUES ($1, $2, $3)"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestRebindMySQL(t *testing.T) {
	query := "INSERT INTO t (a, b) VALUES (?, ?)"
	result := rebind("mysql", query)
	if result != query {
		t.Errorf("expected unchanged query for mysql, got %q", result)
	}
}

func TestRebindSQLite(t *testing.T) {
	query := "INSERT INTO t (a) VALUES (?)"
	result := rebind("sqlite", query)
	if result != query {
		t.Errorf("expected unchanged query for sqlite, got %q", result)
	}
}

func TestRebindEmpty(t *testing.T) {
	query := "INSERT INTO t (a) VALUES (?)"
	result := rebind("", query)
	if result != query {
		t.Errorf("expected unchanged query for empty driver, got %q", result)
	}
}

func TestGenerateNotificationIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := generateNotificationID()
		if seen[id] {
			t.Fatalf("duplicate notification ID generated: %s", id)
		}
		seen[id] = true
	}
}

func TestGenerateNotificationIDLength(t *testing.T) {
	id := generateNotificationID()
	// RFC 4122 canonical UUIDv4: 36 characters, 8-4-4-4-12 hex with dashes.
	if len(id) != 36 {
		t.Errorf("expected 36-char UUID, got %d chars: %s", len(id), id)
	}
	if _, err := uuid.Parse(id); err != nil {
		t.Errorf("generateNotificationID must produce a parseable UUID, got %q: %v", id, err)
	}
}
