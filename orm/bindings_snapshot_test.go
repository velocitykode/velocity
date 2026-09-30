package orm

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// A statement's event is delivered after Exec returns, on the manager's
// pump. A caller that reuses a []byte argument once Exec returned must not
// change what the event reports: QueryExecuted.Bindings holds the bytes
// the statement ran with.
func TestQueryExecuted_BindingsSnapshotByteArguments(t *testing.T) {
	m, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:"})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	if _, err := m.Exec(context.Background(), `CREATE TABLE blobs (data BLOB)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	got := make(chan []QueryBinding, 1)
	m.SetEventDispatcher(func(_ context.Context, ev any) error {
		q, ok := ev.(*QueryExecuted)
		if !ok || !strings.Contains(q.SQL, "INSERT INTO blobs") {
			return nil
		}
		close(entered)
		<-release
		got <- q.Bindings
		return nil
	})

	buf := []byte("original")
	if _, err := m.Exec(context.Background(), `INSERT INTO blobs (data) VALUES (?)`, buf); err != nil {
		t.Fatalf("insert: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(hostile.Deadline):
		t.Fatal("the statement's event was never delivered")
	}
	// The caller reuses its buffer while the event is still queued.
	copy(buf, "OVERRIDE")
	close(release)
	var bindings []QueryBinding
	select {
	case bindings = <-got:
	case <-time.After(hostile.Deadline):
		t.Fatal("the listener did not finish")
	}
	if len(bindings) != 1 {
		t.Fatalf("Bindings = %#v, want one value", bindings)
	}
	want := QueryBinding{Type: "[]uint8", Value: hex.EncodeToString([]byte("original"))}
	if bindings[0] != want {
		t.Fatalf("Bindings[0] = %#v, want %#v, the bytes the statement ran with", bindings[0], want)
	}
}
