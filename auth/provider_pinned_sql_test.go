package auth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"sync"
	"testing"
)

// recordedQueries captures the exact SQL text handed to the driver by the
// most recent provider call. The recording driver appends here; tests reset
// it before each provider method so the slice holds only that call's query.
var recordedQueries []string

var registerRecordingDriverOnce sync.Once

// recordingDriver is a database/sql driver that records every query/exec SQL
// string verbatim and returns empty results, so tests can pin the precise SQL
// a provider emits without a live database.
type recordingDriver struct{}

func (recordingDriver) Open(string) (driver.Conn, error) { return recordingConn{}, nil }

type recordingConn struct{}

func (recordingConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (recordingConn) Close() error                        { return nil }
func (recordingConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

func (recordingConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	recordedQueries = append(recordedQueries, query)
	return emptyRows{}, nil
}

func (recordingConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	recordedQueries = append(recordedQueries, query)
	return driver.RowsAffected(0), nil
}

type emptyRows struct{}

func (emptyRows) Columns() []string {
	return []string{"id", "name", "email", "password", "remember_token"}
}
func (emptyRows) Close() error              { return nil }
func (emptyRows) Next([]driver.Value) error { return io.EOF }

func recordingDB(t *testing.T) *sql.DB {
	t.Helper()
	registerRecordingDriverOnce.Do(func() {
		sql.Register("auth-recording", recordingDriver{})
	})
	db, err := sql.Open("auth-recording", "")
	if err != nil {
		t.Fatalf("open recording driver: %v", err)
	}
	return db
}

// TestORMUserProviderPinnedSQL pins the exact SQL emitted by FindByID,
// FindByCredentials, and UpdateRememberToken for each placeholder dialect.
// Unlike the SQLite end-to-end test (which accepts both ? and $N), the
// recording driver asserts the literal query text, so a provider method that
// hardcoded PostgreSQL placeholders while ph() stayed correct would fail here.
func TestORMUserProviderPinnedSQL(t *testing.T) {
	cases := []struct {
		dialect        string
		findByID       string
		findByCreds    string
		updateRemember string
	}{
		{
			dialect:        "postgres",
			findByID:       "SELECT id, name, email, password, remember_token FROM users WHERE id = $1",
			findByCreds:    "SELECT id, name, email, password, remember_token FROM users WHERE email = $1",
			updateRemember: "UPDATE users SET remember_token = $1 WHERE id = $2",
		},
		{
			dialect:        "mysql",
			findByID:       "SELECT id, name, email, password, remember_token FROM users WHERE id = ?",
			findByCreds:    "SELECT id, name, email, password, remember_token FROM users WHERE email = ?",
			updateRemember: "UPDATE users SET remember_token = ? WHERE id = ?",
		},
		{
			dialect:        "sqlite",
			findByID:       "SELECT id, name, email, password, remember_token FROM users WHERE id = ?",
			findByCreds:    "SELECT id, name, email, password, remember_token FROM users WHERE email = ?",
			updateRemember: "UPDATE users SET remember_token = ? WHERE id = ?",
		},
	}

	assertQuery := func(t *testing.T, method, want string) {
		t.Helper()
		if len(recordedQueries) != 1 {
			t.Fatalf("%s: recorded %d queries, want 1: %q", method, len(recordedQueries), recordedQueries)
		}
		if recordedQueries[0] != want {
			t.Errorf("%s SQL = %q, want %q", method, recordedQueries[0], want)
		}
	}

	for _, tc := range cases {
		t.Run(tc.dialect, func(t *testing.T) {
			db := recordingDB(t)
			defer db.Close()
			p := NewORMUserProviderForDialect(db, "User", nil, tc.dialect)
			user := &AuthUser{ID: 1}

			recordedQueries = nil
			if _, err := p.FindByID(1); err != ErrUserNotFound {
				t.Fatalf("FindByID error = %v, want ErrUserNotFound", err)
			}
			assertQuery(t, "FindByID", tc.findByID)

			recordedQueries = nil
			if _, err := p.FindByCredentials(map[string]interface{}{"email": "test@example.com"}); err != ErrUserNotFound {
				t.Fatalf("FindByCredentials error = %v, want ErrUserNotFound", err)
			}
			assertQuery(t, "FindByCredentials", tc.findByCreds)

			recordedQueries = nil
			if err := p.UpdateRememberToken(user, "tok"); err != nil {
				t.Fatalf("UpdateRememberToken: %v", err)
			}
			assertQuery(t, "UpdateRememberToken", tc.updateRemember)
		})
	}
}
